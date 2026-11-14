package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type turnResult struct {
	Text        string
	CostUSD     float64
	SessionID   string
	IsError     bool
	ErrMsg      string
	APIErrMsg   string
	InTokens    int
	OutTokens   int
	CacheRead   int
	CacheCreate int
	acted       bool
}

type turnParser struct {
	agents      map[string]bool
	apiError    string
	textEmitted bool
	acted       bool
	sessionID   string
}

func newTurnParser() *turnParser { return &turnParser{agents: make(map[string]bool)} }

func readTurn(ctx context.Context, lines <-chan readLine, done <-chan struct{}, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	parser := newTurnParser()
	for {
		select {
		case <-ctx.Done():
			return parser.progress(), ctx.Err()
		case <-done:
			return parser.progress(), io.ErrClosedPipe
		case ln, ok := <-lines:
			turn, finished, err := parser.consume(ln, ok, onEvent)
			if err != nil {
				return parser.progress(), err
			}
			if finished {
				return turn, nil
			}
		}
	}
}

func (p *turnParser) progress() turnResult {
	return turnResult{acted: p.acted, SessionID: p.sessionID}
}

func (p *turnParser) consume(line readLine, open bool, onEvent func(contracts.BackendEvent)) (turnResult, bool, error) {
	if !open {
		return turnResult{}, false, io.ErrUnexpectedEOF
	}
	if len(line.data) > 0 {
		turn, done, err := p.parseLine(line.data, onEvent)
		if err != nil || done {
			return turn, done, err
		}
	}
	if line.err != nil {
		return turnResult{}, false, line.err
	}
	return turnResult{}, false, nil
}

func (p *turnParser) parseLine(line []byte, onEvent func(contracts.BackendEvent)) (turnResult, bool, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return turnResult{}, false, fmt.Errorf("%w: decoding stream event type: %w", ErrStreamDrift, err)
		}
		return turnResult{}, false, nil
	}
	p.noteSessionID(line)
	switch head.Type {
	case wireSystem:
		if isAPIRetry(line) {
			p.acted = true
		}
	case wireAssistant:
		p.acted = true
		p.parseAssistant(line, onEvent)
	case wireUser:
		p.acted = true
		p.parseToolResults(line, onEvent)
	case "result":
		turn, done, err := parseResult(line, onEvent)
		turn.APIErrMsg = p.apiError
		return turn, done, err
	}
	return turnResult{}, false, nil
}

func (p *turnParser) noteSessionID(line []byte) {
	var carrier struct {
		SessionID string `json:"session_id"`
	}
	if decodeOrSkip(line, &carrier) && carrier.SessionID != "" {
		p.sessionID = carrier.SessionID
	}
}

func isAPIRetry(line []byte) bool {
	var system struct {
		Subtype string `json:"subtype"`
	}
	return decodeOrSkip(line, &system) && system.Subtype == subtypeAPIRetry
}

func (p *turnParser) parseAssistant(line []byte, onEvent func(contracts.BackendEvent)) {
	var event streamEvent
	if !decodeOrSkip(line, &event) {
		return
	}
	if event.IsAPIError {
		p.recordAPIError(event.Message.Content)
		return
	}
	if onEvent == nil {
		return
	}
	for _, b := range event.Message.Content {
		p.emitBlock(b, onEvent)
	}
	if u := event.Message.Usage; u != nil {
		onEvent(contracts.BackendEvent{Kind: "usage", InTokens: u.InputTokens, OutTokens: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheCreate: u.CacheCreationInputTokens})
	}
}

func (p *turnParser) recordAPIError(content []contentBlock) {
	var texts []string
	for _, b := range content {
		if t := strings.TrimSpace(b.Text); b.Type == blockText && t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) > 0 {
		p.apiError = strings.Join(texts, "\n")
	}
}

func (p *turnParser) emitBlock(block contentBlock, onEvent func(contracts.BackendEvent)) {
	switch block.Type {
	case "thinking":
		if t := strings.TrimSpace(block.Thinking); t != "" {
			onEvent(contracts.BackendEvent{Kind: "thinking", Detail: t})
		}
	case blockText:
		if t := strings.TrimSpace(block.Text); t != "" {
			onEvent(contracts.BackendEvent{Kind: "text", Detail: p.nextTextDetail(t)})
		}
	case blockToolUse:
		p.emitToolUse(block, onEvent)
	}
}

func (p *turnParser) nextTextDetail(text string) string {
	if !p.textEmitted {
		p.textEmitted = true
		return text
	}
	return textBlockSeparator + text
}

func (p *turnParser) emitToolUse(block contentBlock, onEvent func(contracts.BackendEvent)) {
	switch block.Name {
	case "TodoWrite":
		if todos := parseTodos(block.Input); len(todos) > 0 {
			onEvent(contracts.BackendEvent{Kind: "todos", Todos: todos})
		}
	case "Task":
		agent := parseAgent(block.ID, block.Input)
		p.agents[block.ID] = true
		onEvent(contracts.BackendEvent{Kind: "subagent", Subagent: &agent})
	default:
		onEvent(contracts.BackendEvent{Kind: "tool", Tool: block.Name, Detail: toolDetail(block.Input)})
	}
}

func (p *turnParser) parseToolResults(line []byte, onEvent func(contracts.BackendEvent)) {
	if onEvent == nil {
		return
	}
	var event streamEvent
	if !decodeOrSkip(line, &event) {
		return
	}
	for _, block := range event.Message.Content {
		if block.Type != blockToolResult || !p.agents[block.ToolUseID] {
			continue
		}
		delete(p.agents, block.ToolUseID)
		onEvent(contracts.BackendEvent{Kind: "subagent", Subagent: &contracts.Subagent{ID: block.ToolUseID, State: "done"}})
	}
}

func parseResult(line []byte, onEvent func(contracts.BackendEvent)) (turnResult, bool, error) {
	var control resultControl
	if err := json.Unmarshal(line, &control); err != nil {
		return turnResult{}, false, fmt.Errorf("%w: decoding result event: %w", ErrStreamDrift, err)
	}
	var event streamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return turnResult{}, false, fmt.Errorf("%w: decoding result event: %w", ErrStreamDrift, err)
		}
	}
	tokens := resultUsage(event)
	if onEvent != nil {
		onEvent(contracts.BackendEvent{Kind: "result", Cost: event.TotalCostUSD, IsError: control.IsError, InTokens: tokens.InputTokens, OutTokens: tokens.OutputTokens, CacheRead: tokens.CacheReadInputTokens, CacheCreate: tokens.CacheCreationInputTokens, Models: turnModels(event.ModelUsage)})
	}
	turn := turnResult{
		Text:        control.Result,
		CostUSD:     event.TotalCostUSD,
		SessionID:   control.SessionID,
		IsError:     control.IsError,
		InTokens:    tokens.InputTokens,
		OutTokens:   tokens.OutputTokens,
		CacheRead:   tokens.CacheReadInputTokens,
		CacheCreate: tokens.CacheCreationInputTokens,
	}
	if control.IsError {
		turn.ErrMsg = control.Result
		if strings.TrimSpace(turn.ErrMsg) == "" {
			turn.ErrMsg = joinedErrors(control.Errors)
		}
	}
	return turn, true, nil
}
