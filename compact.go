package claude

import (
	"context"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const (
	compactCommand = "/compact"
	compactNote    = "Contexte compacté, taille après compaction mesurée au prochain tour."
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
		Args: []contracts.Arg{contracts.Text("Reason", e.cause.Error())},
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
