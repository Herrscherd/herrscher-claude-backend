package claude

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var _ contracts.HistoryEditor = (*streamResponder)(nil)

func (r *streamResponder) ReadHistory(ctx context.Context) (contracts.History, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	builder, _, err := r.loadTranscript(ctx)
	if err != nil {
		return contracts.History{}, err
	}
	return builder.history, nil
}

func (r *streamResponder) PruneHistory(ctx context.Context, edits []contracts.HistoryEdit) (contracts.PruneOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	builder, transcript, err := r.loadTranscript(ctx)
	if err != nil {
		return contracts.PruneOutcome{}, err
	}
	plan, err := planEdits(builder, edits)
	if err != nil {
		return contracts.PruneOutcome{}, err
	}
	outcome := pruneOutcome(builder.history, plan)
	if outcome.Edited == 0 {
		return outcome, nil
	}
	if r.sess != nil {
		if dropErr := r.dropSession(); dropErr != nil {
			return contracts.PruneOutcome{}, dropErr
		}
	}
	if pruneErr := pruneTranscript(*transcript, builder.onChain, plan); pruneErr != nil {
		return contracts.PruneOutcome{}, pruneErr
	}
	return outcome, nil
}

func (r *streamResponder) loadTranscript(ctx context.Context) (*historyBuilder, *transcriptFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sessionID := r.currentSessionID()
	if r.sess != nil && r.sess.sessID != "" {
		sessionID = r.sess.sessID
	}
	if sessionID == "" {
		return newHistoryBuilder(newChainIndex()), nil, nil
	}
	root, err := configDir(r.env)
	if err != nil {
		return nil, nil, err
	}
	transcript, err := transcriptPath(root, sessionID)
	if err != nil {
		return nil, nil, err
	}
	builder, err := readTranscript(transcript)
	if err != nil {
		return nil, nil, err
	}
	return builder, &transcript, nil
}
