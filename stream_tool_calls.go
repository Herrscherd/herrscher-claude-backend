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
	maxCallIDBytes    = 256
	maxToolNameBytes  = 256
)

type onceWarning struct {
	message  string
	attrs    []any
	reported bool
}

func (w *onceWarning) warnOnce() {
	if w.reported {
		return
	}
	w.reported = true
	slog.Warn(w.message, w.attrs...)
}

func boundWarning(name string, limit int) onceWarning {
	return onceWarning{
		message: "tool calls reach a bound of the turn, later starts carry no lifecycle",
		attrs:   []any{"bound", name, "limit", limit},
	}
}

func (p *turnParser) toolStart(block contentBlock) contracts.BackendEvent {
	detail := toolDetail(block.Input)
	event := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: block.Name, Detail: detail}
	reserved := p.reserveCallID(block.ID)
	if !reserved || !openableToolName(block.Name) {
		return event
	}
	if len(p.openCalls) >= maxOpenToolCalls {
		p.openBound.warnOnce()
		return event
	}
	event.ToolCall = &contracts.ToolCall{ID: block.ID, Tool: block.Name, Detail: detail, State: contracts.ToolCallStarted}
	p.openCalls[block.ID] = block.Name
	return event
}

func openableToolName(name string) bool {
	return name != "" && len(name) <= maxToolNameBytes
}

func (p *turnParser) callIDSeen(callID string) bool {
	_, seen := p.seenCalls[callID]
	return seen
}

func (p *turnParser) reserveCallID(callID string) bool {
	if callID == "" || len(callID) > maxCallIDBytes {
		return false
	}
	if p.callIDSeen(callID) {
		return false
	}
	if len(p.seenCalls) >= maxSeenToolCalls {
		p.seenBound.warnOnce()
		return false
	}
	p.seenCalls[callID] = struct{}{}
	return true
}

func (p *turnParser) toolFinish(block toolResultBlock) (contracts.BackendEvent, bool) {
	tool, open := p.openCalls[block.ToolUseID]
	if !open {
		return contracts.BackendEvent{}, false
	}
	delete(p.openCalls, block.ToolUseID)
	if block.IsError.malformed {
		p.flagWarning.warnOnce()
		return contracts.BackendEvent{}, false
	}
	call := &contracts.ToolCall{ID: block.ToolUseID, Tool: tool, State: contracts.ToolCallSucceeded}
	if block.IsError.failed {
		call.State = contracts.ToolCallFailed
		call.Error = truncateRunes(strings.TrimSpace(toolResultText(block.Content)), maxToolErrorRunes)
	}
	return contracts.BackendEvent{Kind: contracts.BackendKindToolDone, ToolCall: call}, true
}

func (p *turnParser) refuseDuplicateTask(callID string) {
	p.refusedTasks[callID] = struct{}{}
	p.duplicateTaskWarning.warnOnce()
}

func (p *turnParser) swallowRefusedTask(callID string) bool {
	if _, refused := p.refusedTasks[callID]; !refused {
		return false
	}
	delete(p.refusedTasks, callID)
	return true
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
