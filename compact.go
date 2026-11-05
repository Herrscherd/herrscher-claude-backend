package claude

import (
	"context"
	"strings"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var (
	_ contracts.Compactor   = (*streamResponder)(nil)
	_ contracts.NoticeError = (*compactUnavailableError)(nil)
)

const (
	compactCommand        = "/compact"
	compactNote           = "Context compacted, its size after compaction is measured on the next turn."
	maxCompactReasonRunes = 200
)

type compactUnavailableError struct {
	cause error
}

func (e *compactUnavailableError) Error() string {
	return "claude compaction: " + e.cause.Error()
}

func (e *compactUnavailableError) Unwrap() error {
	return e.cause
}

func (e *compactUnavailableError) Notice() contracts.Notice {
	return contracts.Notice{
		Code: contracts.CodeCompactUnavailable,
		Args: []contracts.Arg{contracts.Text("Reason", compactReason(e.cause))},
	}
}

func (r *streamResponder) Compact(ctx context.Context, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn, err := r.runTurn(ctx, compactMessage(req.Instruction), onEvent)
	if err != nil {
		return contracts.CompactResult{}, &compactUnavailableError{cause: err}
	}
	if turn.IsError {
		return contracts.CompactResult{}, &compactUnavailableError{cause: errFromTurn(turn, turn.SessionID)}
	}
	return contracts.CompactResult{
		Note:   compactNote,
		Notice: contracts.Notice{Code: contracts.CodeCompacted},
	}, nil
}

func compactMessage(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return compactCommand
	}
	return compactCommand + " " + instruction
}

func compactReason(cause error) string {
	for line := range strings.SplitSeq(cause.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncateRunes(line, maxCompactReasonRunes)
		}
	}
	return ""
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}
