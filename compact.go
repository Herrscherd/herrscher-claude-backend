package claude

import (
	"context"
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const (
	compactCommand = "/compact"
	compactNote    = "Contexte compacté, taille après compaction mesurée au prochain tour."
)

func (r *streamResponder) Compact(ctx context.Context, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn, err := r.runTurn(ctx, compactMessage(req.Instruction), onEvent)
	if err != nil {
		return contracts.CompactResult{}, fmt.Errorf("claude compaction: %w", err)
	}
	if turn.IsError {
		return contracts.CompactResult{}, fmt.Errorf("claude compaction: %w", errFromTurn(turn, turn.SessionID))
	}
	return contracts.CompactResult{Note: compactNote}, nil
}

func compactMessage(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return compactCommand
	}
	return compactCommand + " " + instruction
}
