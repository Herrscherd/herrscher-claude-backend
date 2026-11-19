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

func closeWithin(tb testing.TB, sess *streamSession) error {
	tb.Helper()
	return returnWithin(tb, "Close", sess.Close)
}

type pruneResult struct {
	outcome contracts.PruneOutcome
	err     error
}

func pruneWithin(tb testing.TB, r *streamResponder, edits []contracts.HistoryEdit) (contracts.PruneOutcome, error) {
	tb.Helper()
	res := returnWithin(tb, "PruneHistory", func() pruneResult {
		outcome, err := r.PruneHistory(tb.Context(), edits)
		return pruneResult{outcome: outcome, err: err}
	})
	return res.outcome, res.err
}

func mustWriteString(tb testing.TB, writer io.Writer, text string) {
	tb.Helper()
	if _, err := io.WriteString(writer, text); err != nil {
		tb.Errorf("write: %v", err)
	}
}

func mustClose(tb testing.TB, closer io.Closer) {
	tb.Helper()
	if err := returnWithin(tb, "close", closer.Close); err != nil {
		tb.Errorf("close: %v", err)
	}
}

func respondWithin(ctx context.Context, tb testing.TB, b contracts.Backend, prompt contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	tb.Helper()
	type respondResult struct {
		text string
		err  error
	}
	res := returnWithin(tb, "Respond", func() respondResult {
		text, err := b.Respond(ctx, prompt, onEvent)
		return respondResult{text: text, err: err}
	})
	return res.text, res.err
}

func compactWithin(ctx context.Context, tb testing.TB, r *streamResponder, req contracts.CompactRequest, onEvent func(contracts.BackendEvent)) (contracts.CompactResult, error) {
	tb.Helper()
	type compactResult struct {
		result contracts.CompactResult
		err    error
	}
	res := returnWithin(tb, "Compact", func() compactResult {
		result, err := r.Compact(ctx, req, onEvent)
		return compactResult{result: result, err: err}
	})
	return res.result, res.err
}
