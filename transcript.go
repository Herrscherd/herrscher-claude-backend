package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
// only a write landing between that last check and the rename escapes it.
var ErrTranscriptChanged = errors.New("claude session transcript changed while being pruned")

const (
	configDirVar        = "CLAUDE_CONFIG_DIR"
	transcriptBackupExt = ".herrscher-bak"
	transcriptStagedExt = ".herrscher-tmp"
	prunedResultNote    = "[herrscher pruned the rest of this result: %d characters]"
	prunedCallNote      = "[herrscher pruned the result of this call]"
	prunedInputRunes    = 200
)

var sessionIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,127}$`)

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
	resultContent map[string]json.RawMessage
	chain         map[string]bool
	onChain       map[int]bool
	boundaryAt    int
}

func newHistoryBuilder(transcript transcriptFile, read os.FileInfo, index *chainIndex) *historyBuilder {
	return &historyBuilder{
		file:          transcript,
		read:          read,
		callAt:        make(map[string][2]int),
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
	b.resultContent[block.ToolUseID] = block.Content
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

func readTranscript(ctx context.Context, transcript transcriptFile) (*historyBuilder, error) {
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
	if err = eachTranscriptLine(ctx, io.NewSectionReader(file, 0, info.Size()), func(lineIndex int, line []byte) error {
		index.add(lineIndex, line)
		return nil
	}); err != nil {
		return nil, err
	}
	builder := newHistoryBuilder(transcript, info, index)
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

func prunedResult(result string, keep int) (string, bool) {
	head := runeHead(result, keep)
	if len(head) == len(result) {
		return result, false
	}
	return joinText(head, fmt.Sprintf(prunedResultNote, utf8.RuneCountInString(result[len(head):]))), true
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
	encoded, err := json.Marshal(value)
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

	content json.RawMessage
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func prunedContent(edit contracts.HistoryEdit, content json.RawMessage) (json.RawMessage, error) {
	if len(content) == 0 {
		return nil, nil
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return prunedTextContent(edit, text)
	}
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		return nil, nil
	}
	var pruned []json.RawMessage
	var err error
	if edit.Action == contracts.PruneCall {
		pruned, err = noteBlocks(blocks)
	} else {
		pruned, err = truncatedBlocks(blocks, edit.KeepChars)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(pruned)
	if err != nil {
		return nil, fmt.Errorf("encode pruned claude tool result: %w", err)
	}
	if resultText(encoded) == resultText(content) {
		return nil, nil
	}
	return encoded, nil
}

func prunedTextContent(edit contracts.HistoryEdit, text string) (json.RawMessage, error) {
	result, changed := prunedCallNote, text != prunedCallNote
	if edit.Action != contracts.PruneCall {
		result, changed = prunedResult(text, edit.KeepChars)
	}
	if !changed {
		return nil, nil
	}
	encoded, err := json.Marshal(result)
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
	note, err := json.Marshal(textBlock{Type: blockText, Text: prunedCallNote})
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

func truncatedBlocks(blocks []json.RawMessage, keep int) ([]json.RawMessage, error) {
	total := 0
	for _, block := range blocks {
		if text, isText := blockOfText(block); isText {
			total += utf8.RuneCountInString(text)
		}
	}
	if total <= keep {
		return blocks, nil
	}
	kept := make([]json.RawMessage, 0, len(blocks))
	budget, cut := keep, false
	for _, block := range blocks {
		text, isText := blockOfText(block)
		switch {
		case !isText:
			kept = append(kept, block)
		case cut:
		case utf8.RuneCountInString(text) <= budget:
			kept = append(kept, block)
			budget -= utf8.RuneCountInString(text)
		default:
			rewritten, err := withText(block, joinText(runeHead(text, budget), fmt.Sprintf(prunedResultNote, total-keep)))
			if err != nil {
				return nil, err
			}
			kept = append(kept, rewritten)
			cut = true
		}
	}
	return kept, nil
}

func withText(block json.RawMessage, text string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		return nil, fmt.Errorf("decode claude text block: %w", err)
	}
	encodedText, err := json.Marshal(text)
	if err != nil {
		return nil, fmt.Errorf("encode claude text block: %w", err)
	}
	fields["text"] = encodedText
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode claude text block: %w", err)
	}
	return encoded, nil
}

func pruneOutcome(history contracts.History, edits map[string]pruneEdit) contracts.PruneOutcome {
	pruned := contracts.History{Messages: make([]contracts.HistoryMessage, len(history.Messages))}
	edited := 0
	for index, message := range history.Messages {
		message.Calls = append([]contracts.HistoryCall(nil), message.Calls...)
		for callIndex := range message.Calls {
			call := &message.Calls[callIndex]
			edit, ok := edits[call.ID]
			if !ok {
				continue
			}
			input := call.Input
			if edit.Action == contracts.PruneCall {
				input = prunedInput(call.Input)
			}
			if edit.content != nil || input != call.Input {
				edited++
			}
			if edit.content != nil {
				call.Result = resultText(edit.content)
			}
			call.Input = input
		}
		pruned.Messages[index] = message
	}
	return contracts.PruneOutcome{Edited: edited, CharsBefore: historyRunes(history), CharsAfter: historyRunes(pruned)}
}

func planEdits(read *historyBuilder, edits []contracts.HistoryEdit) (map[string]pruneEdit, error) {
	if err := contracts.ValidateHistoryEdits(edits); err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	for _, call := range read.history.Calls() {
		known[call.ID] = true
	}
	plan := make(map[string]pruneEdit, len(edits))
	for _, edit := range edits {
		if !known[edit.CallID] {
			return nil, fmt.Errorf("%w: %s", contracts.ErrUnknownHistoryCall, edit.CallID)
		}
		content, err := prunedContent(edit, read.resultContent[edit.CallID])
		if err != nil {
			return nil, err
		}
		plan[edit.CallID] = pruneEdit{HistoryEdit: edit, content: content}
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
	if err = checkUnchanged(root, read.file.name, read.read); err != nil {
		return err
	}
	stagedName := read.file.name + transcriptStagedExt
	removeStaged(root, stagedName)
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
	backup := name + transcriptBackupExt
	if err := root.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("drop old claude transcript backup: %w", err)
	}
	if err := root.Link(name, backup); err != nil {
		return fmt.Errorf("back up claude transcript: %w", err)
	}
	if err := checkUnchanged(root, name, read); err != nil {
		removeStaged(root, backup)
		return err
	}
	if err := root.Rename(stagedName, name); err != nil {
		return fmt.Errorf("replace claude transcript: %w", err)
	}
	return nil
}

func closeAndLog(closer io.Closer) {
	if err := closer.Close(); err != nil {
		slog.Warn("closing a claude transcript", "error", err)
	}
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
			line = prunedLine(line, plan)
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

func prunedLine(line []byte, plan map[string]pruneEdit) []byte {
	var entry transcriptEntry
	if json.Unmarshal(line, &entry) != nil || !entry.inView() {
		return line
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil {
		return line
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(fields["message"], &message) != nil {
		return line
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(message["content"], &blocks) != nil {
		return line
	}
	if !pruneBlocks(blocks, plan) {
		return line
	}
	content, contentErr := json.Marshal(blocks)
	message["content"] = content
	encodedMessage, messageErr := json.Marshal(message)
	fields["message"] = encodedMessage
	encoded, lineErr := json.Marshal(fields)
	if err := errors.Join(contentErr, messageErr, lineErr); err != nil {
		return line
	}
	return append(encoded, line[len(bytes.TrimRight(line, "\r\n")):]...)
}

func pruneBlocks(blocks []map[string]json.RawMessage, plan map[string]pruneEdit) bool {
	changed := false
	for _, block := range blocks {
		var kind string
		if json.Unmarshal(block["type"], &kind) != nil {
			continue
		}
		switch kind {
		case blockToolUse:
			changed = pruneToolUse(block, plan) || changed
		case blockToolResult:
			changed = pruneToolResult(block, plan) || changed
		}
	}
	return changed
}

func pruneToolUse(block map[string]json.RawMessage, plan map[string]pruneEdit) bool {
	var callID string
	if json.Unmarshal(block["id"], &callID) != nil || plan[callID].Action != contracts.PruneCall {
		return false
	}
	return replaceRaw(block, "input", json.RawMessage(prunedInput(string(block["input"]))))
}

func pruneToolResult(block map[string]json.RawMessage, plan map[string]pruneEdit) bool {
	var callID string
	if json.Unmarshal(block["tool_use_id"], &callID) != nil {
		return false
	}
	edit, ok := plan[callID]
	if !ok || edit.content == nil {
		return false
	}
	return replaceRaw(block, "content", edit.content)
}

func replaceRaw(block map[string]json.RawMessage, key string, value json.RawMessage) bool {
	if bytes.Equal(block[key], value) {
		return false
	}
	block[key] = value
	return true
}
