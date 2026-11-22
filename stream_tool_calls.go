package claude

import (
	"encoding/json"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	maxOpenToolCalls  = 256
	maxToolErrorRunes = 200
)

func (p *turnParser) toolStart(block contentBlock) contracts.BackendEvent {
	detail := toolDetail(block.Input)
	event := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: block.Name, Detail: detail}
	if block.ID == "" || block.Name == "" || p.isOpen(block.ID) {
		return event
	}
	event.ToolCall = &contracts.ToolCall{ID: block.ID, Tool: block.Name, Detail: detail, State: contracts.ToolCallStarted}
	p.openToolCall(block.ID, block.Name)
	return event
}

func (p *turnParser) isOpen(callID string) bool {
	_, open := p.openCalls[callID]
	return open
}

func (p *turnParser) openToolCall(callID, tool string) {
	if len(p.openCalls) < maxOpenToolCalls {
		p.openCalls[callID] = tool
	}
}

func (p *turnParser) toolFinish(block contentBlock) (contracts.BackendEvent, bool) {
	tool, open := p.openCalls[block.ToolUseID]
	if !open {
		return contracts.BackendEvent{}, false
	}
	delete(p.openCalls, block.ToolUseID)
	call := &contracts.ToolCall{ID: block.ToolUseID, Tool: tool, State: contracts.ToolCallSucceeded}
	if block.IsError {
		call.State = contracts.ToolCallFailed
		call.Error = truncateRunes(strings.TrimSpace(toolResultText(block.Content)), maxToolErrorRunes)
	}
	return contracts.BackendEvent{Kind: contracts.BackendKindToolDone, ToolCall: call}, true
}

func toolResultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []contentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	texts := make([]string, 0, len(blocks))
	for idx := range blocks {
		if blocks[idx].Type == blockText && blocks[idx].Text != "" {
			texts = append(texts, blocks[idx].Text)
		}
	}
	return strings.Join(texts, "\n")
}
