package claude

import (
	"context"
	"io"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// TestInterrupt_PreservesSessionForResume verifies that cancelling a turn's ctx
// mid-flight (the path an interrupt frame drives) drops the wedged session but
// preserves its conversation id in resumeID, so the next turn resumes the same
// conversation rather than starting a brand-new one.
func TestInterrupt_PreservesSessionForResume(t *testing.T) {
	pr, pw := io.Pipe()
	sess := newStreamSession(discardWriteCloser{}, pr)
	sess.sessID = "conv-1" // a session id already observed this conversation
	r := &streamResponder{ctx: context.Background(), sess: sess}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// One assistant chunk, then interrupt mid-turn (no terminal result).
		_, _ = pw.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}` + "\n"))
		cancel()
	}()

	if _, err := r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil); err == nil {
		t.Fatal("expected a cancellation error from the interrupted turn")
	}
	if r.sess != nil {
		t.Fatal("interrupted session should be dropped")
	}
	if r.resumeID != "conv-1" {
		t.Fatalf("resumeID not preserved for resume: got %q, want %q", r.resumeID, "conv-1")
	}
	_ = pw.Close()
}
