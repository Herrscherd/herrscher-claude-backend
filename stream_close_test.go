package claude

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type spyWriteCloser struct {
	writeErr error
	closeErr error
	closed   bool
}

func (s *spyWriteCloser) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return len(p), nil
}

func (s *spyWriteCloser) Close() error {
	s.closed = true
	return s.closeErr
}

func startTestSession(t *testing.T, name string, args ...string) *streamSession {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	s := newStreamSession(stdin, stdout)
	s.cmd = cmd
	return s
}

func TestStreamSessionCloseTwiceReturnsNil(t *testing.T) {
	t.Parallel()
	s := startTestSession(t, "sh", "-c", "sleep 60")
	if err := s.Close(); err != nil {
		t.Fatalf("first Close returned %v, want nil after a kill", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close returned %v, want nil", err)
	}
}

func TestStreamSessionCloseAfterNormalExitReturnsNil(t *testing.T) {
	t.Parallel()
	s := startTestSession(t, "sh", "-c", "exit 0")
	for range s.lines {
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after a normal exit returned %v, want nil", err)
	}
}

func TestRespondClosesRestartedSessionWhenItsSendFails(t *testing.T) {
	writeErr := errors.New("broken pipe")
	fresh := &spyWriteCloser{writeErr: writeErr}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(discardWriteCloser{}, strings.NewReader(""))
	r.spawn = func(string) (*streamSession, error) {
		return newStreamSession(fresh, strings.NewReader("")), nil
	}

	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want the restarted session's write error", err)
	}
	if r.sess != nil {
		t.Fatal("a restarted session whose send failed must not stay installed")
	}
	if !fresh.closed {
		t.Fatal("the restarted session was never closed")
	}
}

func TestRespondLogsOldSessionCloseErrorOnRestart(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dead := newStreamSession(&spyWriteCloser{closeErr: errors.New("close failed")}, strings.NewReader(""))
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = dead
	r.spawn = func(string) (*streamSession, error) {
		reply := `{"type":"result","is_error":false,"result":"ok","session_id":"s2"}` + "\n"
		return newStreamSession(&spyWriteCloser{}, strings.NewReader(reply)), nil
	}

	got, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if err != nil {
		t.Fatalf("Respond after a restart: %v", err)
	}
	if got != "ok" {
		t.Fatalf("text = %q, want ok", got)
	}
	if !strings.Contains(logs.String(), "close failed") {
		t.Fatalf("the dead session's close error was dropped silently; logs: %q", logs.String())
	}
	mustClose(t, r)
}

type endlessLines struct{}

func (endlessLines) Read(p []byte) (int, error) {
	return copy(p, "{}\n"), nil
}

func requireLineReaderStopped(t *testing.T, lines <-chan readLine) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-lines:
			if !open {
				return
			}
		case <-timeout:
			t.Fatal("the line reader goroutine outlived its consumer")
		}
	}
}

func TestStartLineReaderStopsOnceDone(t *testing.T) {
	done := make(chan struct{})
	lines := startLineReader(endlessLines{}, done)
	close(done)
	requireLineReaderStopped(t, lines)
}

func TestStreamSessionCloseStopsItsLineReader(t *testing.T) {
	s := newStreamSession(&spyWriteCloser{}, endlessLines{})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireLineReaderStopped(t, s.lines)
}
