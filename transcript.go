package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// ErrTranscriptNotFound reports a session whose transcript file the claude CLI
// left in no project directory of its config directory.
var ErrTranscriptNotFound = errors.New("claude session transcript not found")

// ErrBadSessionID reports a session id that is not one the claude CLI writes, and
// so never names a transcript file.
var ErrBadSessionID = errors.New("claude session id is malformed")

// ErrTranscriptChanged reports a transcript that another writer, such as a
// `claude --resume` run in a terminal on the same session, changed after it was
// read: PruneHistory leaves it untouched rather than drop what was appended. The
// file is checked when the prune starts and again right before the rename, so
// only a write landing between that last check and the rename escapes it. A
// refused prune also removes the backup link it made, so a transcript never has
// more than its .herrscher-bak and its .herrscher-bak.prev next to it.
var ErrTranscriptChanged = errors.New("claude session transcript changed while being pruned")

// ErrTranscriptBackup reports a prune that could not keep the transcript it
// replaces, most often on a file system without hard links such as exFAT, vfat
// or some FUSE and SMB mounts: PruneHistory leaves the transcript untouched, and
// every later prune of that session fails the same way until it moves to a file
// system with hard links.
var ErrTranscriptBackup = errors.New("claude session transcript could not be backed up")

const (
	configDirVar          = "CLAUDE_CONFIG_DIR"
	transcriptBackupExt   = ".herrscher-bak"
	transcriptPreviousExt = ".herrscher-bak.prev"
	transcriptStagedExt   = ".herrscher-tmp"
	staleStagedAge        = time.Hour
	stagedTimeBase        = 36
	prunedResultNote      = "[herrscher pruned the rest of this result: %d characters]"
	prunedCallNote        = "[herrscher pruned the result of this call]"
	prunedInputRunes      = 200
)

var sessionIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,127}$`)

var prunedResultPattern = regexp.MustCompile(strings.Replace(regexp.QuoteMeta(prunedResultNote), "%d", `\d+`, 1))

func configDir(env map[string]string) (string, error) {
	if dir := env[configDirVar]; dir != "" {
		return dir, nil
	}
	if dir := os.Getenv(configDirVar); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude config directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

type transcriptFile struct {
	dir  string
	name string
}

func transcriptPath(configRoot, sessionID string) (transcriptFile, error) {
	if !sessionIDPattern.MatchString(sessionID) {
		return transcriptFile{}, fmt.Errorf("%w: %q", ErrBadSessionID, sessionID)
	}
	name := sessionID + ".jsonl"
	projects := filepath.Join(configRoot, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return transcriptFile{}, fmt.Errorf("claude transcript lookup: %w", err)
	}
	newest, newestTime := "", int64(0)
	for _, entry := range entries {
		candidate := filepath.Join(projects, entry.Name(), name)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if stamp := info.ModTime().UnixNano(); newest == "" || stamp > newestTime {
			newest, newestTime = candidate, stamp
		}
	}
	if newest == "" {
		return transcriptFile{}, fmt.Errorf("%w: session %s under %s", ErrTranscriptNotFound, sessionID, configRoot)
	}
	return transcriptFile{dir: filepath.Dir(newest), name: name}, nil
}

type transcriptEntry struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type transcriptMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type transcriptBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (e transcriptEntry) inView() bool {
	return !e.IsSidechain && (e.Type == wireUser || e.Type == wireAssistant)
}

func (e transcriptEntry) isCompactBoundary() bool {
	return e.Type == "system" && e.Subtype == "compact_boundary"
}

func eachTranscriptLine(ctx context.Context, source io.Reader, visit func(lineIndex int, line []byte) error) error {
	reader := bufio.NewReader(source)
	for lineIndex := 0; ; lineIndex++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("claude transcript at line %d: %w", lineIndex, err)
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if visitErr := visit(lineIndex, line); visitErr != nil {
				return visitErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read claude transcript: %w", err)
		}
	}
}

type chainIndex struct {
	parents    map[string]string
	leaf       string
	boundaryAt int
}

func newChainIndex() *chainIndex {
	return &chainIndex{parents: make(map[string]string), boundaryAt: -1}
}

func (c *chainIndex) add(lineIndex int, line []byte) {
	var entry transcriptEntry
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	if entry.isCompactBoundary() {
		clear(c.parents)
		c.leaf = ""
		c.boundaryAt = lineIndex
		return
	}
	if entry.UUID == "" || entry.IsSidechain {
		return
	}
	c.parents[entry.UUID] = entry.ParentUUID
	c.leaf = entry.UUID
}

func (c *chainIndex) mainChain() map[string]bool {
	chain := make(map[string]bool)
	uuid := c.leaf
	for uuid != "" && !chain[uuid] {
		parent, ok := c.parents[uuid]
		if !ok {
			break
		}
		chain[uuid] = true
		uuid = parent
	}
	return chain
}

type historyBuilder struct {
	file          transcriptFile
	read          os.FileInfo
	history       contracts.History
	lastMsgID     string
	callAt        map[string][2]int
	keepResults   map[string]bool
	resultContent map[string]json.RawMessage
	chain         map[string]bool
	onChain       map[int]bool
	boundaryAt    int
}

func newHistoryBuilder(transcript transcriptFile, read os.FileInfo, index *chainIndex, keepResults map[string]bool) *historyBuilder {
	return &historyBuilder{
		file:          transcript,
		read:          read,
		callAt:        make(map[string][2]int),
		keepResults:   keepResults,
		resultContent: make(map[string]json.RawMessage),
		chain:         index.mainChain(),
		onChain:       make(map[int]bool),
		boundaryAt:    index.boundaryAt,
	}
}

func (b *historyBuilder) add(lineIndex int, line []byte) {
	if lineIndex <= b.boundaryAt {
		return
	}
	var entry transcriptEntry
	if json.Unmarshal(line, &entry) != nil || !entry.inView() {
		return
	}
	var message transcriptMessage
	if json.Unmarshal(entry.Message, &message) != nil || !b.followsChain(entry, message) {
		return
	}
	b.onChain[lineIndex] = true
	if entry.Type == wireAssistant {
		b.addAssistant(message)
		return
	}
	b.addUser(message)
}

func (b *historyBuilder) followsChain(entry transcriptEntry, message transcriptMessage) bool {
	if b.chain[entry.UUID] {
		return true
	}
	return entry.Type == wireUser && b.chain[entry.ParentUUID] && onlyToolResults(message.Content)
}

func onlyToolResults(content json.RawMessage) bool {
	var blocks []transcriptBlock
	if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	for index := range blocks {
		if blocks[index].Type != blockToolResult {
			return false
		}
	}
	return true
}

func (b *historyBuilder) addAssistant(message transcriptMessage) {
	if message.ID == "" || message.ID != b.lastMsgID {
		b.history.Messages = append(b.history.Messages, contracts.HistoryMessage{Role: contracts.RoleAssistant})
	}
	b.lastMsgID = message.ID
	current := &b.history.Messages[len(b.history.Messages)-1]
	var blocks []transcriptBlock
	if json.Unmarshal(message.Content, &blocks) != nil {
		return
	}
	for index := range blocks {
		block := &blocks[index]
		switch block.Type {
		case blockText:
			current.Text = joinText(current.Text, block.Text)
		case blockToolUse:
			b.callAt[block.ID] = [2]int{len(b.history.Messages) - 1, len(current.Calls)}
			current.Calls = append(current.Calls, contracts.HistoryCall{ID: block.ID, Tool: block.Name, Input: string(block.Input)})
		}
	}
}

func (b *historyBuilder) addUser(message transcriptMessage) {
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		b.lastMsgID = ""
		b.history.Messages = append(b.history.Messages, contracts.HistoryMessage{Role: contracts.RoleUser, Text: text})
		return
	}
	var blocks []transcriptBlock
	if json.Unmarshal(message.Content, &blocks) != nil {
		b.lastMsgID = ""
		return
	}
	for index := range blocks {
		block := &blocks[index]
		switch block.Type {
		case blockText:
			text = joinText(text, block.Text)
		case blockToolResult:
			b.attachResult(block)
		}
	}
	if text != "" {
		b.lastMsgID = ""
		b.history.Messages = append(b.history.Messages, contracts.HistoryMessage{Role: contracts.RoleUser, Text: text})
	}
}

func (b *historyBuilder) attachResult(block *transcriptBlock) {
	position, ok := b.callAt[block.ToolUseID]
	if !ok {
		return
	}
	call := &b.history.Messages[position[0]].Calls[position[1]]
	call.Result = resultText(block.Content)
	call.ResultIsError = block.IsError
	if b.keepResults[block.ToolUseID] {
		b.resultContent[block.ToolUseID] = block.Content
	}
}

func joinText(have, more string) string {
	if have == "" {
		return more
	}
	if more == "" {
		return have
	}
	return have + "\n" + more
}

func resultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []transcriptBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var joined string
	for index := range blocks {
		if blocks[index].Type == blockText {
			joined = joinText(joined, blocks[index].Text)
		}
	}
	return joined
}

func readTranscript(ctx context.Context, transcript transcriptFile, keepResults map[string]bool) (*historyBuilder, error) {
	root, err := os.OpenRoot(transcript.dir)
	if err != nil {
		return nil, fmt.Errorf("open claude transcript directory: %w", err)
	}
	defer closeAndLog(root)
	file, err := root.Open(transcript.name)
	if err != nil {
		return nil, fmt.Errorf("open claude transcript: %w", err)
	}
	defer closeAndLog(file)
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat claude transcript: %w", err)
	}
	index := newChainIndex()
	if indexErr := eachTranscriptLine(ctx, io.NewSectionReader(file, 0, info.Size()), func(lineIndex int, line []byte) error {
		index.add(lineIndex, line)
		return nil
	}); indexErr != nil {
		return nil, indexErr
	}
	builder := newHistoryBuilder(transcript, info, index, keepResults)
	err = eachTranscriptLine(ctx, io.NewSectionReader(file, 0, info.Size()), func(lineIndex int, line []byte) error {
		builder.add(lineIndex, line)
		return nil
	})
	return builder, err
}

func historyRunes(history contracts.History) int {
	total := 0
	for _, call := range history.Calls() {
		total += utf8.RuneCountInString(call.Input) + utf8.RuneCountInString(call.Result)
	}
	return total
}

func runeHead(text string, keep int) string {
	kept := 0
	for offset := range text {
		if kept == keep {
			return text[:offset]
		}
		kept++
	}
	return text
}

func runeTail(text string, keep int) string {
	if keep <= 0 {
		return ""
	}
	skip := max(0, utf8.RuneCountInString(text)-keep)
	seen := 0
	for offset := range text {
		if seen == skip {
			return text[offset:]
		}
		seen++
	}
	return ""
}

func textRunes(blocks []json.RawMessage) int {
	total := 0
	for _, block := range blocks {
		if text, isText := blockOfText(block); isText {
			total += utf8.RuneCountInString(text)
		}
	}
	return total
}

func prunedResult(result string, keep, keepTail int) (string, bool) {
	total := utf8.RuneCountInString(result)
	if keep+keepTail >= total {
		return result, false
	}
	note := fmt.Sprintf(prunedResultNote, total-keep-keepTail)
	return joinText(joinText(runeHead(result, keep), note), runeTail(result, keepTail)), true
}

func alreadyPruned(result string, keep, keepTail int) bool {
	if result == prunedCallNote {
		return true
	}
	note := prunedResultPattern.FindStringIndex(result)
	if note == nil {
		return false
	}
	head := strings.TrimSuffix(result[:note[0]], "\n")
	tail := strings.TrimPrefix(result[note[1]:], "\n")
	return utf8.RuneCountInString(head) <= keep && utf8.RuneCountInString(tail) <= keepTail
}

func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func prunedInput(input string) string {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return input
	}
	value, shortened := shortenStrings(value)
	if !shortened {
		return input
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return input
	}
	return string(encoded)
}

func shortenStrings(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if head := runeHead(typed, prunedInputRunes); len(head) < len(typed) {
			return head + "…", true
		}
		return typed, false
	case map[string]any:
		shortenedAny := false
		for key, inner := range typed {
			var shortened bool
			typed[key], shortened = shortenStrings(inner)
			shortenedAny = shortenedAny || shortened
		}
		return typed, shortenedAny
	case []any:
		shortenedAny := false
		for index, inner := range typed {
			var shortened bool
			typed[index], shortened = shortenStrings(inner)
			shortenedAny = shortenedAny || shortened
		}
		return typed, shortenedAny
	}
	return value, false
}

type pruneEdit struct {
	contracts.HistoryEdit

	input   string
	content json.RawMessage
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func contentBlocks(content json.RawMessage) ([]json.RawMessage, bool) {
	var blocks []json.RawMessage
	return blocks, json.Unmarshal(content, &blocks) == nil
}

func prunedContent(edit contracts.HistoryEdit, content json.RawMessage) (json.RawMessage, error) {
	before := resultText(content)
	if edit.Action != contracts.PruneCall && alreadyPruned(before, edit.KeepChars, edit.KeepTail) {
		return nil, nil
	}
	rewritten, err := rewrittenContent(edit, content)
	if err != nil || rewritten == nil {
		return nil, err
	}
	if utf8.RuneCountInString(resultText(rewritten)) >= utf8.RuneCountInString(before) {
		return nil, nil
	}
	return rewritten, nil
}

func rewrittenContent(edit contracts.HistoryEdit, content json.RawMessage) (json.RawMessage, error) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return prunedTextContent(edit, text)
	}
	blocks, isList := contentBlocks(content)
	if !isList {
		return nil, nil
	}
	var pruned []json.RawMessage
	var err error
	if edit.Action == contracts.PruneCall {
		pruned, err = noteBlocks(blocks)
	} else {
		pruned, err = truncatedBlocks(blocks, edit.KeepChars, edit.KeepTail)
	}
	if err != nil {
		return nil, err
	}
	return jsonArray(pruned), nil
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	array := []byte{'['}
	for index, item := range items {
		if index > 0 {
			array = append(array, ',')
		}
		array = append(array, item...)
	}
	return append(array, ']')
}

func prunedTextContent(edit contracts.HistoryEdit, text string) (json.RawMessage, error) {
	result, changed := prunedCallNote, text != prunedCallNote
	if edit.Action != contracts.PruneCall {
		result, changed = prunedResult(text, edit.KeepChars, edit.KeepTail)
	}
	if !changed {
		return nil, nil
	}
	encoded, err := encodeJSON(result)
	if err != nil {
		return nil, fmt.Errorf("encode pruned claude tool result: %w", err)
	}
	return encoded, nil
}

func blockOfText(raw json.RawMessage) (string, bool) {
	var block textBlock
	if json.Unmarshal(raw, &block) != nil || block.Type != blockText {
		return "", false
	}
	return block.Text, true
}

func noteBlocks(blocks []json.RawMessage) ([]json.RawMessage, error) {
	note, err := encodeJSON(textBlock{Type: blockText, Text: prunedCallNote})
	if err != nil {
		return nil, fmt.Errorf("encode pruned claude tool result: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(blocks)+1)
	placed := false
	for _, block := range blocks {
		if _, isText := blockOfText(block); !isText {
			kept = append(kept, block)
			continue
		}
		if !placed {
			kept = append(kept, note)
			placed = true
		}
	}
	if !placed {
		kept = append(kept, note)
	}
	return kept, nil
}

func truncatedBlocks(blocks []json.RawMessage, keep, keepTail int) ([]json.RawMessage, error) {
	total := textRunes(blocks)
	if keep+keepTail >= total {
		return blocks, nil
	}
	tailStart := total - keepTail
	note := fmt.Sprintf(prunedResultNote, total-keep-keepTail)
	kept := make([]json.RawMessage, 0, len(blocks))
	offset := 0
	for _, block := range blocks {
		text, isText := blockOfText(block)
		if !isText {
			kept = append(kept, block)
			continue
		}
		start := offset
		offset += utf8.RuneCountInString(text)
		if offset <= keep || start >= tailStart {
			kept = append(kept, block)
			continue
		}
		cutText := joinText(joinText(runeHead(text, max(0, keep-start)), note), runeTail(text, offset-max(start, tailStart)))
		note = ""
		if cutText == "" {
			continue
		}
		rewritten, err := withText(block, cutText)
		if err != nil {
			return nil, err
		}
		kept = append(kept, rewritten)
	}
	return kept, nil
}

func withText(block json.RawMessage, text string) (json.RawMessage, error) {
	encodedText, err := encodeJSON(text)
	if err != nil {
		return nil, fmt.Errorf("encode claude text block: %w", err)
	}
	rewritten, err := replaceField(block, "text", encodedText)
	if err != nil {
		return nil, err
	}
	return rewritten.bytes, nil
}

type byteSpan struct {
	start int
	end   int
	found bool
}

type rewrite struct {
	bytes   []byte
	changed bool
}

func fieldSpan(object []byte, key string) (byteSpan, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	opening, err := decoder.Token()
	if err != nil {
		return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", err)
	}
	if opening != json.Delim('{') {
		return byteSpan{}, nil
	}
	for decoder.More() {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", tokenErr)
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return byteSpan{}, fmt.Errorf("decode claude transcript object: %w", decodeErr)
		}
		if name, isKey := token.(string); isKey && name == key {
			end := int(decoder.InputOffset())
			return byteSpan{start: end - len(value), end: end, found: true}, nil
		}
	}
	return byteSpan{}, nil
}

func splice(object []byte, span byteSpan, value []byte) []byte {
	spliced := make([]byte, 0, len(object)-(span.end-span.start)+len(value))
	spliced = append(spliced, object[:span.start]...)
	spliced = append(spliced, value...)
	return append(spliced, object[span.end:]...)
}

func replaceField(object []byte, key string, value []byte) (rewrite, error) {
	span, err := fieldSpan(object, key)
	if err != nil || !span.found || bytes.Equal(object[span.start:span.end], value) {
		return rewrite{bytes: object}, err
	}
	return rewrite{bytes: splice(object, span, value), changed: true}, nil
}

func prunedCall(call contracts.HistoryCall, edit pruneEdit) contracts.HistoryCall {
	if edit.input != "" {
		call.Input = edit.input
	}
	if edit.content != nil {
		call.Result = resultText(edit.content)
	}
	return call
}

func pruneOutcome(history contracts.History, edits map[string]pruneEdit) contracts.PruneOutcome {
	pruned := contracts.History{Messages: make([]contracts.HistoryMessage, len(history.Messages))}
	edited := 0
	for index, message := range history.Messages {
		message.Calls = append([]contracts.HistoryCall(nil), message.Calls...)
		for callIndex, call := range message.Calls {
			edit, ok := edits[call.ID]
			if !ok {
				continue
			}
			if pruned := prunedCall(call, edit); pruned != call {
				message.Calls[callIndex] = pruned
				edited++
			}
		}
		pruned.Messages[index] = message
	}
	return contracts.PruneOutcome{Edited: edited, CharsBefore: historyRunes(history), CharsAfter: historyRunes(pruned)}
}

func plannedInput(edit contracts.HistoryEdit, input string) string {
	if edit.Action != contracts.PruneCall {
		return ""
	}
	if pruned := prunedInput(input); utf8.RuneCountInString(pruned) < utf8.RuneCountInString(input) {
		return pruned
	}
	return ""
}

func planEdits(read *historyBuilder, edits []contracts.HistoryEdit) (map[string]pruneEdit, error) {
	if err := contracts.ValidateHistoryEdits(edits); err != nil {
		return nil, err
	}
	inputs := make(map[string]string)
	for _, call := range read.history.Calls() {
		inputs[call.ID] = call.Input
	}
	plan := make(map[string]pruneEdit, len(edits))
	for _, edit := range edits {
		input, known := inputs[edit.CallID]
		if !known {
			return nil, fmt.Errorf("%w: %s", contracts.ErrUnknownHistoryCall, edit.CallID)
		}
		content, err := prunedContent(edit, read.resultContent[edit.CallID])
		if err != nil {
			return nil, err
		}
		plan[edit.CallID] = pruneEdit{HistoryEdit: edit, input: plannedInput(edit, input), content: content}
	}
	return plan, nil
}

func pruneTranscript(ctx context.Context, read *historyBuilder, plan map[string]pruneEdit) error {
	root, err := os.OpenRoot(read.file.dir)
	if err != nil {
		return fmt.Errorf("open claude transcript directory: %w", err)
	}
	defer closeAndLog(root)
	source, err := root.Open(read.file.name)
	if err != nil {
		return fmt.Errorf("open claude transcript: %w", err)
	}
	defer closeAndLog(source)
	if changedErr := checkUnchanged(root, read.file.name, read.read); changedErr != nil {
		return changedErr
	}
	removeStaleStaged(root, read.file.name)
	stagedName := stagingName(read.file.name)
	staged, err := root.OpenFile(stagedName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, read.read.Mode().Perm())
	if err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	defer removeStaged(root, stagedName)
	if err = writePrunedTranscript(ctx, io.NewSectionReader(source, 0, read.read.Size()), staged, read.onChain, plan); err != nil {
		return errors.Join(err, staged.Close())
	}
	if err = staged.Sync(); err != nil {
		return errors.Join(fmt.Errorf("stage claude transcript: %w", err), staged.Close())
	}
	if err = staged.Close(); err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	return swapTranscript(root, read.file.name, stagedName, read.read)
}

func checkUnchanged(root *os.Root, name string, read os.FileInfo) error {
	current, err := root.Stat(name)
	if err != nil {
		return fmt.Errorf("stat claude transcript: %w", err)
	}
	if !os.SameFile(read, current) || read.Size() != current.Size() || !read.ModTime().Equal(current.ModTime()) {
		return fmt.Errorf("%w: %s", ErrTranscriptChanged, name)
	}
	return nil
}

func swapTranscript(root *os.Root, name, stagedName string, read os.FileInfo) error {
	backup, err := backUpTranscript(root, name)
	if err != nil {
		return err
	}
	if err = checkUnchanged(root, name, read); err != nil {
		removeStaged(root, backup.link)
		return err
	}
	if backup.replaces != "" {
		if err = root.Rename(backup.link, backup.replaces); err != nil {
			removeStaged(root, backup.link)
			return fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
		}
	}
	if err = root.Rename(stagedName, name); err != nil {
		return fmt.Errorf("replace claude transcript: %w", err)
	}
	if syncErr := syncDirectory(root); syncErr != nil {
		slog.Warn("claude transcript pruned, but its directory was not synced: a crash may bring the old transcript back", "transcript", name, "error", syncErr)
	}
	logKeptBackups(root, name)
	return nil
}

type transcriptBackup struct {
	link     string
	replaces string
}

func backUpTranscript(root *os.Root, name string) (transcriptBackup, error) {
	original := name + transcriptBackupExt
	err := root.Link(name, original)
	if err == nil {
		return transcriptBackup{link: original}, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return transcriptBackup{}, fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
	}
	previous := name + transcriptPreviousExt
	pending := stagingName(previous)
	if err = root.Link(name, pending); err != nil {
		return transcriptBackup{}, fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
	}
	return transcriptBackup{link: pending, replaces: previous}, nil
}

func logKeptBackups(root *os.Root, name string) {
	var kept int64
	for _, backup := range []string{name + transcriptBackupExt, name + transcriptPreviousExt} {
		if info, err := root.Stat(backup); err == nil {
			kept += info.Size()
		}
	}
	slog.Info("claude transcript pruned, backups kept", "transcript", name, "backup_bytes", kept)
}

func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("sync claude transcript directory: %w", err)
	}
	if err = dir.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync claude transcript directory: %w", err), dir.Close())
	}
	if err = dir.Close(); err != nil {
		return fmt.Errorf("sync claude transcript directory: %w", err)
	}
	return nil
}

func closeAndLog(closer io.Closer) {
	if err := closer.Close(); err != nil {
		slog.Warn("closing a claude transcript", "error", err)
	}
}

func stagingName(name string) string {
	return name + transcriptStagedExt + "." + strconv.FormatInt(time.Now().Unix(), stagedTimeBase) + "." + rand.Text()
}

func removeStaleStaged(root *os.Root, name string) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		slog.Warn("listing staged claude transcripts", "error", err)
		return
	}
	for _, entry := range entries {
		for _, prefix := range []string{name + transcriptStagedExt, name + transcriptPreviousExt + transcriptStagedExt} {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if staged, known := stagedTime(entry, prefix); known && time.Since(staged) > staleStagedAge {
				removeStaged(root, entry.Name())
			}
			break
		}
	}
}

func stagedTime(entry fs.DirEntry, prefix string) (time.Time, bool) {
	stamp, _, named := strings.Cut(strings.TrimPrefix(entry.Name(), prefix+"."), ".")
	if seconds, err := strconv.ParseInt(stamp, stagedTimeBase, 64); named && err == nil {
		return time.Unix(seconds, 0), true
	}
	info, err := entry.Info()
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func removeStaged(root *os.Root, name string) {
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("removing a staged claude transcript", "name", name, "error", err)
	}
}

func writePrunedTranscript(ctx context.Context, source io.Reader, target io.Writer, onChain map[int]bool, plan map[string]pruneEdit) error {
	writer := bufio.NewWriter(target)
	err := eachTranscriptLine(ctx, source, func(lineIndex int, line []byte) error {
		if onChain[lineIndex] {
			pruned, pruneErr := prunedLine(line, plan)
			if pruneErr != nil {
				return fmt.Errorf("claude transcript at line %d: %w", lineIndex, pruneErr)
			}
			line = pruned
		}
		if _, writeErr := writer.Write(line); writeErr != nil {
			return fmt.Errorf("write claude transcript: %w", writeErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = writer.Flush(); err != nil {
		return fmt.Errorf("write claude transcript: %w", err)
	}
	return nil
}

func prunedLine(line []byte, plan map[string]pruneEdit) ([]byte, error) {
	entry := bytes.TrimRight(line, "\r\n")
	messageSpan, err := fieldSpan(entry, "message")
	if err != nil || !messageSpan.found {
		return line, err
	}
	message := entry[messageSpan.start:messageSpan.end]
	contentSpan, err := fieldSpan(message, "content")
	if err != nil || !contentSpan.found {
		return line, err
	}
	content, err := prunedBlocks(message[contentSpan.start:contentSpan.end], plan)
	if err != nil || !content.changed {
		return line, err
	}
	pruned := splice(entry, messageSpan, splice(message, contentSpan, content.bytes))
	return append(pruned, line[len(entry):]...), nil
}

func prunedBlocks(content json.RawMessage, plan map[string]pruneEdit) (rewrite, error) {
	if !bytes.HasPrefix(content, []byte("[")) {
		return rewrite{bytes: content}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return rewrite{}, fmt.Errorf("decode claude message content: %w", err)
	}
	changed := false
	for index, block := range blocks {
		rewritten, err := prunedBlock(block, plan)
		if err != nil {
			return rewrite{}, err
		}
		blocks[index] = rewritten.bytes
		changed = changed || rewritten.changed
	}
	if !changed {
		return rewrite{bytes: content}, nil
	}
	return rewrite{bytes: jsonArray(blocks), changed: true}, nil
}

type blockHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

func prunedBlock(block json.RawMessage, plan map[string]pruneEdit) (rewrite, error) {
	var header blockHeader
	if err := json.Unmarshal(block, &header); err != nil {
		return rewrite{}, fmt.Errorf("decode claude message block: %w", err)
	}
	switch header.Type {
	case blockToolUse:
		if input := plan[header.ID].input; input != "" {
			return replaceField(block, "input", []byte(input))
		}
	case blockToolResult:
		if content := plan[header.ToolUseID].content; content != nil {
			return replaceField(block, "content", content)
		}
	}
	return rewrite{bytes: block}, nil
}
