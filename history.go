package claude

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.HistoryEditor = (*streamResponder)(nil)

func (r *streamResponder) ReadHistory(ctx context.Context) (contracts.History, error) {
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	read, err := r.loadTranscript(ctx, nil)
	if err != nil {
		return contracts.History{}, err
	}
	return read.history, nil
}

func (r *streamResponder) PruneHistory(ctx context.Context, edits []contracts.HistoryEdit) (contracts.PruneOutcome, error) {
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	plan, err := r.planPrune(ctx, edits)
	if err != nil || plan.outcome.Edited == 0 {
		return plan.outcome, err
	}
	if session := r.currentSession(); session != nil {
		if dropErr := r.dropSession(session); dropErr != nil {
			return contracts.PruneOutcome{}, dropErr
		}
		plan, err = r.planPrune(ctx, edits)
		if err != nil || plan.outcome.Edited == 0 {
			return plan.outcome, err
		}
	}
	if pruneErr := pruneTranscript(ctx, plan.read, plan.edits); pruneErr != nil {
		return contracts.PruneOutcome{}, pruneErr
	}
	return plan.outcome, nil
}

type prunePlan struct {
	read    *historyBuilder
	edits   map[string]pruneEdit
	outcome contracts.PruneOutcome
}

func (r *streamResponder) planPrune(ctx context.Context, edits []contracts.HistoryEdit) (prunePlan, error) {
	read, err := r.loadTranscript(ctx, editedCalls(edits))
	if err != nil {
		return prunePlan{}, err
	}
	planned, err := planEdits(read, edits)
	if err != nil {
		return prunePlan{}, err
	}
	return prunePlan{read: read, edits: planned, outcome: pruneOutcome(read.history, planned)}, nil
}

func editedCalls(edits []contracts.HistoryEdit) map[string]bool {
	calls := make(map[string]bool, len(edits))
	for _, edit := range edits {
		calls[edit.CallID] = true
	}
	return calls
}

func (r *streamResponder) loadTranscript(ctx context.Context, keepResults map[string]bool) (*historyBuilder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID := r.currentSessionID()
	if session := r.currentSession(); session != nil && session.sessID != "" {
		sessionID = session.sessID
	}
	if sessionID == "" {
		return newHistoryBuilder(transcriptFile{}, nil, newChainIndex(), keepResults), nil
	}
	root, err := configDir(r.env)
	if err != nil {
		return nil, err
	}
	transcript, err := transcriptPath(root, sessionID)
	if err != nil {
		return nil, err
	}
	return readTranscript(ctx, transcript, keepResults)
}
