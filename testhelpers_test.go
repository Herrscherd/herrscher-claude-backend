package claude

import (
	"io"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const returnDeadline = 10 * time.Second

func returnWithin[T any](t *testing.T, what string, call func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- call() }()
	select {
	case res := <-done:
		return res
	case <-time.After(returnDeadline):
		t.Fatalf("%s did not return within 10 seconds", what)
		var zero T
		return zero
	}
}

func awaitWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	return returnWithin(t, what, func() T { return <-ch })
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
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
