package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type transcriptEntry struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	IsSidechain bool            `json:"isSidechain"`
	IsAPIError  apiErrorFlag    `json:"isApiErrorMessage"`
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
		if !entry.IsAPIError {
			b.addAssistant(message)
		}
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
			current.Text = joinParagraphs(current.Text, block.Text)
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
	return joinWith("\n", have, more)
}

func joinParagraphs(have, more string) string {
	trimmed := strings.TrimSpace(more)
	if trimmed == "" {
		return have
	}
	return joinWith(textBlockSeparator, have, trimmed)
}

func joinWith(separator, have, more string) string {
	if have == "" {
		return more
	}
	if more == "" {
		return have
	}
	return have + separator + more
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
