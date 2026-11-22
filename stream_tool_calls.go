package claude

import (
	"encoding/json"
	"log/slog"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	maxOpenToolCalls  = 256
	maxSeenToolCalls  = 4096
	maxToolErrorRunes = 200
)

func (p *turnParser) toolStart(block contentBlock) contracts.BackendEvent {
	detail := toolDetail(block.Input)
	event := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: block.Name, Detail: detail}
	if !p.claimCallID(block.ID) {
		return event
	}
	if block.Name == "" {
		return event
	}
	if len(p.openCalls) >= maxOpenToolCalls {
		p.reportOpenBound()
		return event
	}
	event.ToolCall = &contracts.ToolCall{ID: block.ID, Tool: block.Name, Detail: detail, State: contracts.ToolCallStarted}
	p.openCalls[block.ID] = block.Name
	return event
}

func (p *turnParser) claimCallID(callID string) bool {
	if callID == "" {
		return false
	}
	if _, seen := p.seenCalls[callID]; seen {
		return false
	}
	if len(p.seenCalls) >= maxSeenToolCalls {
		p.reportSeenBound()
		return false
	}
	p.seenCalls[callID] = struct{}{}
	return true
}

func (p *turnParser) reportOpenBound() {
	if p.openBoundReported {
		return
	}
	p.openBoundReported = true
	slog.Warn("tool calls reach the open bound of the turn, later starts carry no lifecycle", "max_open", maxOpenToolCalls)
}

func (p *turnParser) reportSeenBound() {
	if p.seenBoundReported {
		return
	}
	p.seenBoundReported = true
	slog.Warn("tool calls reach the seen bound of the turn, later starts carry no lifecycle", "max_seen", maxSeenToolCalls)
}

func (p *turnParser) toolFinish(block toolResultBlock) (contracts.BackendEvent, bool) {
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
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range decodeElements[contentBlock](parts) {
		if part.Type == blockText && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}
