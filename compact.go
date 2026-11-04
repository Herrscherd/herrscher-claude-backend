package claude

import (
	"context"
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.Compactor = (*streamResponder)(nil)

const compactCommand = "/compact"

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
	return compactOutcome(turn), nil
}

func compactMessage(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return compactCommand
	}
	return compactCommand + " " + instruction
}

func compactOutcome(turn turnResult) contracts.CompactResult {
	if !turn.UsageReported {
		return contracts.CompactResult{Note: "Contexte compacté, taille non mesurée."}
	}
	tokens := turn.InTokens + turn.CacheRead + turn.CacheCreate
	return contracts.CompactResult{
		TokensAfter:    tokens,
		TokensMeasured: true,
		Note:           fmt.Sprintf("Contexte compacté, %d tokens.", tokens),
	}
}
