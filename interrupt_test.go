package claude

import (
	"context"
	"io"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func TestInterrupt_PreservesSessionForResume(t *testing.T) {
	pr, pw := io.Pipe()
	sess := newStreamSession(discardWriteCloser{}, pr)
	sess.sessID = "conv-1"
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = sess

	ctx, cancel := context.WithCancel(context.Background())
	go mustWriteString(t, pw, `{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`+"\n")

	onEvent := func(contracts.BackendEvent) { cancel() }
	if _, err := respondWithin(ctx, t, r, contracts.Prompt{Content: "hi"}, onEvent); err == nil {
		t.Fatal("expected a cancellation error from the interrupted turn")
	}
	if r.sess != nil {
		t.Fatal("interrupted session should be dropped")
	}
	if r.resumeID != "conv-1" {
		t.Fatalf("resumeID not preserved for resume: got %q, want %q", r.resumeID, "conv-1")
	}
	mustClose(t, pw)
}
