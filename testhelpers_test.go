package claude

import (
	"context"
	"io"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const returnDeadline = 10 * time.Second

func returnWithin[T any](tb testing.TB, what string, call func() T) T {
	tb.Helper()
	done := make(chan T, 1)
	go func() { done <- call() }()
	select {
	case res := <-done:
		return res
	case <-time.After(returnDeadline):
		tb.Fatalf("%s did not return within 10 seconds", what)
		var zero T
		return zero
	}
}

func awaitWithin[T any](tb testing.TB, ch <-chan T, what string) T {
	tb.Helper()
	return returnWithin(tb, what, func() T { return <-ch })
}

func closeWithin(t *testing.T, sess *streamSession) error {
	t.Helper()
	return returnWithin(t, "Close", sess.Close)
}

type pruneResult struct {
	outcome contracts.PruneOutcome
	err     error
}

func pruneWithin(t *testing.T, r *streamResponder, edits []contracts.HistoryEdit) (contracts.PruneOutcome, error) {
	t.Helper()
	res := returnWithin(t, "PruneHistory", func() pruneResult {
		outcome, err := r.PruneHistory(t.Context(), edits)
		return pruneResult{outcome: outcome, err: err}
	})
	return res.outcome, res.err
}

func mustWriteString(t *testing.T, writer io.Writer, text string) {
	t.Helper()
	if _, err := io.WriteString(writer, text); err != nil {
		t.Errorf("write: %v", err)
	}
}

func mustClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := returnWithin(t, "close", closer.Close); err != nil {
		t.Errorf("close: %v", err)
	}
}

func respondWithin(ctx context.Context, t *testing.T, b contracts.Backend, prompt contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	t.Helper()
	type respondResult struct {
		text string
		err  error
	}
	res := returnWithin(t, "Respond", func() respondResult {
		text, err := b.Respond(ctx, prompt, onEvent)
		return respondResult{text: text, err: err}
	})
	return res.text, res.err
}

func compactWithin(ctx context.Context, t *testing.T, r *streamResponder, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	t.Helper()
	type compactResult struct {
		result contracts.CompactResult
		err    error
	}
	res := returnWithin(t, "Compact", func() compactResult {
		result, err := r.Compact(ctx, req, onEvent)
		return compactResult{result: result, err: err}
	})
	return res.result, res.err
}
