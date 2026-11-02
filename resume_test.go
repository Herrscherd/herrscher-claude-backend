package claude

import (
	"context"
	"io"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestStreamResponderResumeToken(t *testing.T) {
	r := &streamResponder{resumeID: "boot-id"}
	if got := r.ResumeToken(); got != "boot-id" {
		t.Fatalf("nil session: want boot-id, got %q", got)
	}
	r.sess = newStreamSession(nopWriteCloser{io.Discard}, strings.NewReader(""))
	r.publishSessionID("live-id")
	if got := r.ResumeToken(); got != "live-id" {
		t.Fatalf("live session: want live-id, got %q", got)
	}
}

func TestStreamResponderIsResumeAware(t *testing.T) {
	var _ contracts.ResumeAware = (*streamResponder)(nil)
}

func TestNewBackendThreadsResumeID(t *testing.T) {
	b, err := NewBackend(context.Background(), Config{Kind: "stream", Cmd: "claude", ResumeID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := b.(*streamResponder)
	if !ok {
		t.Fatalf("want *streamResponder, got %T", b)
	}
	if r.resumeID != "x" {
		t.Fatalf("resumeID not threaded: got %q", r.resumeID)
	}
}
