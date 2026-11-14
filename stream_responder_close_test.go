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

func TestCloseWhileRespondIsInFlight(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, feed := io.Pipe()
	t.Cleanup(func() { mustClose(t, feed) })
	r := newStreamResponder(ctx, nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a responder must not spawn a process once it is closed")
		return nil, errors.New("unexpected spawn")
	}
	r.sess = newStreamSession(&spyWriteCloser{}, stream)

	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	cancel()

	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-responded; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("in-flight Respond err = %v, want the context error or ErrResponderClosed", err)
	}
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "again"}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Respond after the racing Close: err = %v, want ErrResponderClosed", err)
	}
}
