package claude

import (
	"bufio"
	"bytes"
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
	matches, err := filepath.Glob(filepath.Join(configRoot, "projects", "*", name))
	if err != nil {
		return transcriptFile{}, fmt.Errorf("claude transcript lookup: %w", err)
	}
	newest, newestTime := "", int64(0)
	for _, match := range matches {
		info, statErr := os.Stat(match)
		if statErr != nil {
			continue
		}
		if stamp := info.ModTime().UnixNano(); newest == "" || stamp > newestTime {
			newest, newestTime = match, stamp
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

func eachTranscriptLine(source io.Reader, visit func(line []byte) error) error {
	reader := bufio.NewReader(source)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if visitErr := visit(line); visitErr != nil {
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

type historyBuilder struct {
	history    contracts.History
	lastMsgID  string
	callAt     map[string][2]int
	boundaryAt int
}

func newHistoryBuilder() *historyBuilder {
	return &historyBuilder{callAt: make(map[string][2]int), boundaryAt: -1}
}

func (b *historyBuilder) reset(lineIndex int) {
	b.history = contracts.History{}
	b.lastMsgID = ""
	clear(b.callAt)
	b.boundaryAt = lineIndex
}

func (b *historyBuilder) add(lineIndex int, line []byte) {
	var entry transcriptEntry
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	if entry.isCompactBoundary() {
		b.reset(lineIndex)
		return
	}
	if !entry.inView() {
		return
	}
	var message transcriptMessage
	if json.Unmarshal(entry.Message, &message) != nil {
		return
	}
	if entry.Type == wireAssistant {
		b.addAssistant(message)
		return
	}
	b.addUser(message)
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
	b.lastMsgID = ""
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		b.history.Messages = append(b.history.Messages, contracts.HistoryMessage{Role: contracts.RoleUser, Text: text})
		return
	}
	var blocks []transcriptBlock
	if json.Unmarshal(message.Content, &blocks) != nil {
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

func readTranscript(transcript transcriptFile) (*historyBuilder, error) {
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
	builder := newHistoryBuilder()
	lineIndex := 0
	err = eachTranscriptLine(file, func(line []byte) error {
		builder.add(lineIndex, line)
		lineIndex++
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

func prunedResult(result string, keep int) (string, bool) {
	runes := utf8.RuneCountInString(result)
	if runes <= keep {
		return result, false
	}
	head := string([]rune(result)[:keep])
	return joinText(head, fmt.Sprintf(prunedResultNote, runes-keep)), true
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
		if utf8.RuneCountInString(typed) > prunedInputRunes {
			return string([]rune(typed)[:prunedInputRunes]) + "…", true
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

	result string
}

func (e pruneEdit) newResult() (string, bool) {
	if e.Action == contracts.PruneCall {
		return prunedCallNote, e.result != prunedCallNote
	}
	return prunedResult(e.result, e.KeepChars)
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
			result, changed := edit.newResult()
			input := call.Input
			if edit.Action == contracts.PruneCall {
				input = prunedInput(call.Input)
			}
			if changed || input != call.Input {
				edited++
			}
			call.Result, call.Input = result, input
		}
		pruned.Messages[index] = message
	}
	return contracts.PruneOutcome{Edited: edited, CharsBefore: historyRunes(history), CharsAfter: historyRunes(pruned)}
}

func planEdits(history contracts.History, edits []contracts.HistoryEdit) (map[string]pruneEdit, error) {
	if err := contracts.ValidateHistoryEdits(edits); err != nil {
		return nil, err
	}
	results := make(map[string]string)
	for _, call := range history.Calls() {
		results[call.ID] = call.Result
	}
	plan := make(map[string]pruneEdit, len(edits))
	for _, edit := range edits {
		result, ok := results[edit.CallID]
		if !ok {
			return nil, fmt.Errorf("%w: %s", contracts.ErrUnknownHistoryCall, edit.CallID)
		}
		plan[edit.CallID] = pruneEdit{HistoryEdit: edit, result: result}
	}
	return plan, nil
}

func pruneTranscript(transcript transcriptFile, boundaryAt int, plan map[string]pruneEdit) error {
	root, err := os.OpenRoot(transcript.dir)
	if err != nil {
		return fmt.Errorf("open claude transcript directory: %w", err)
	}
	defer closeAndLog(root)
	source, err := root.Open(transcript.name)
	if err != nil {
		return fmt.Errorf("open claude transcript: %w", err)
	}
	defer closeAndLog(source)
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat claude transcript: %w", err)
	}
	stagedName := transcript.name + transcriptStagedExt
	removeStaged(root, stagedName)
	staged, err := root.OpenFile(stagedName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	defer removeStaged(root, stagedName)
	if err = writePrunedTranscript(source, staged, boundaryAt, plan); err != nil {
		return errors.Join(err, staged.Close())
	}
	if err = staged.Sync(); err != nil {
		return errors.Join(fmt.Errorf("stage claude transcript: %w", err), staged.Close())
	}
	if err = staged.Close(); err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	return swapTranscript(root, transcript.name, stagedName)
}

func swapTranscript(root *os.Root, name, stagedName string) error {
	backup := name + transcriptBackupExt
	if err := root.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("drop old claude transcript backup: %w", err)
	}
	if err := root.Link(name, backup); err != nil {
		return fmt.Errorf("back up claude transcript: %w", err)
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

func writePrunedTranscript(source io.Reader, target io.Writer, boundaryAt int, plan map[string]pruneEdit) error {
	writer := bufio.NewWriter(target)
	lineIndex := 0
	err := eachTranscriptLine(source, func(line []byte) error {
		if lineIndex > boundaryAt {
			line = prunedLine(line, plan)
		}
		lineIndex++
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
	if !ok {
		return false
	}
	result, changed := edit.newResult()
	if !changed {
		return false
	}
	encoded, err := json.Marshal(result)
	return err == nil && replaceRaw(block, "content", encoded)
}

func replaceRaw(block map[string]json.RawMessage, key string, value json.RawMessage) bool {
	if bytes.Equal(block[key], value) {
		return false
	}
	block[key] = value
	return true
}
