package claude

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestRespondAfterCloseReturnsErrResponderClosed(t *testing.T) {
	t.Parallel()
	writer := &spyWriteCloser{}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a closed responder must not spawn a process")
		return nil, errors.New("unexpected spawn")
	}
	r.sess = newStreamSession(writer, strings.NewReader(""))
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed", err)
	}
	if writer.writes != 0 {
		t.Fatalf("prompt written %d times after Close, want 0", writer.writes)
	}
}

func TestResponderCloseTwiceReturnsNil(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v, want nil", err)
	}
}

type signalWriteCloser struct {
	wrote chan struct{}
}

func (w signalWriteCloser) Write(p []byte) (int, error) {
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (signalWriteCloser) Close() error { return nil }

func TestCloseWhileRespondIsInFlight(t *testing.T) {
	t.Parallel()
	stream, feed := io.Pipe()
	t.Cleanup(func() { mustClose(t, feed) })
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a responder must not spawn a process once it is closed")
		return nil, errors.New("unexpected spawn")
	}
	wrote := make(chan struct{}, 1)
	r.sess = newStreamSession(signalWriteCloser{wrote: wrote}, stream)

	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	awaitWithin(t, wrote, "the prompt reaching the CLI")
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := awaitWithin(t, responded, "Respond in flight during Close"); !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("in-flight Respond err = %v, want ErrResponderClosed", err)
	}
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "again"}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Respond after Close: err = %v, want ErrResponderClosed", err)
	}
}

func TestCompactAfterCloseReturnsErrResponderClosedWithoutSpawning(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a closed responder must not spawn a process to compact")
		return nil, errors.New("unexpected spawn")
	}
	r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := r.Compact(context.Background(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed", err)
	}
	if notice := contracts.NoticeOf(err); notice.Code != contracts.CodeCompactUnavailable {
		t.Fatalf("Notice.Code = %v, want CodeCompactUnavailable", notice.Code)
	}
}
