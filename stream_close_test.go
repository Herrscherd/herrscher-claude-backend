package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type spyWriteCloser struct {
	writeErr error
	closeErr error
	closed   bool
	writes   int
}

func (s *spyWriteCloser) Write(p []byte) (int, error) {
	s.writes++
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return len(p), nil
}

func (s *spyWriteCloser) Close() error {
	s.closed = true
	return s.closeErr
}

func startShellSession(t *testing.T, script string) *streamSession {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sh -c %q: %v", script, err)
	}
	s := newStreamSession(stdin, stdout)
	s.cmd = cmd
	return s
}

func TestStreamSessionCloseTwiceReturnsNil(t *testing.T) {
	t.Parallel()
	s := startShellSession(t, "sleep 60")
	if err := s.Close(); err != nil {
		t.Fatalf("first Close returned %v, want nil after a kill", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close returned %v, want nil", err)
	}
}

func TestStreamSessionCloseAfterNormalExitReturnsNil(t *testing.T) {
	t.Parallel()
	s := startShellSession(t, "exit 0")
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

func TestRespondDoesNotResendAfterStreamDrift(t *testing.T) {
	assistantLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}` + "\n"
	overBoundLine := func() io.Reader {
		line, _ := lineSource(maxStreamLineBytes)
		return line
	}
	for _, tc := range []struct {
		name   string
		stream io.Reader
		want   error
	}{
		{"drifted result", strings.NewReader(`{"type":"result","is_error":"false","result":"ok","session_id":"s"}` + "\n"), ErrStreamDrift},
		{"drifted intermediate line", strings.NewReader(`{"type":7}` + "\n" + `{"type":"result","is_error":false,"result":"ok","session_id":"s"}` + "\n"), ErrStreamDrift},
		{"line over the size limit", io.MultiReader(strings.NewReader(assistantLine), overBoundLine()), ErrStreamLineTooLong},
		{"line over the size limit before any event", overBoundLine(), ErrStreamLineTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &spyWriteCloser{}
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			r.sess = newStreamSession(writer, tc.stream)
			r.spawn = func(string) (*streamSession, error) {
				t.Error("a stream that ends the turn must not restart the session")
				return nil, errors.New("unexpected spawn")
			}
			var kinds []string
			_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, func(e contracts.BackendEvent) {
				kinds = append(kinds, e.Kind)
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if writer.writes != 1 {
				t.Fatalf("prompt written %d times, want 1", writer.writes)
			}
			if slices.Contains(kinds, "reset") {
				t.Fatalf("a drifted stream emitted a reset: %v", kinds)
			}
			requireSessionDropped(t, r, writer)
		})
	}
}

func requireSessionDropped(t *testing.T, r *streamResponder, writer *spyWriteCloser) {
	t.Helper()
	if r.sess != nil {
		t.Fatal("a drifted session must not stay installed")
	}
	if !writer.closed {
		t.Fatal("the session was left open: its stdin was never closed")
	}
}

func TestRespondStopsProcessOnOverBoundLine(t *testing.T) {
	t.Parallel()
	script := fmt.Sprintf("head -c %d /dev/zero; exec sleep 60", maxStreamLineBytes+lineReadBufferBytes)
	s := startShellSession(t, script)
	process := s.cmd
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = s
	r.spawn = func(string) (*streamSession, error) {
		t.Error("an over-bound line must not restart the session")
		return nil, errors.New("unexpected spawn")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil)
		returned <- err
	}()
	var err error
	select {
	case err = <-returned:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Respond did not return within 10s of an over-bound line")
	}
	if !errors.Is(err, ErrStreamLineTooLong) {
		t.Fatalf("err = %v, want ErrStreamLineTooLong", err)
	}
	if process.ProcessState == nil {
		t.Fatal("the claude process was not killed and reaped")
	}
	if r.sess != nil {
		t.Fatal("a session that ended on an over-bound line must not stay installed")
	}
}

const partialAssistantLine = `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}` + "\n"

func TestRetryStopsAfterOneRetry(t *testing.T) {
	overBoundRetry := func() io.Reader {
		line, _ := lineSource(maxStreamLineBytes)
		return line
	}
	plainEndRetry := func() io.Reader { return strings.NewReader(partialAssistantLine) }
	for _, tc := range []struct {
		name  string
		retry func() io.Reader
		want  error
		kinds []string
	}{
		{"retry hits an over-bound line", overBoundRetry, ErrStreamLineTooLong, []string{"text", "reset"}},
		{"retry ends at a plain end of stream after text", plainEndRetry, io.EOF, []string{"text", "reset", "text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireRetryStops(t, tc.retry, tc.want, tc.kinds)
		})
	}
}

func requireRetryStops(t *testing.T, retry func() io.Reader, want error, wantKinds []string) {
	t.Helper()
	first := &spyWriteCloser{}
	second := &spyWriteCloser{}
	spawns := 0
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(first, strings.NewReader(partialAssistantLine))
	r.spawn = func(string) (*streamSession, error) {
		spawns++
		if spawns > 1 {
			t.Error("a retry that fails must not start another attempt")
			return nil, errors.New("unexpected spawn")
		}
		return newStreamSession(second, retry()), nil
	}
	var kinds []string
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, func(e contracts.BackendEvent) {
		kinds = append(kinds, e.Kind)
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("events = %v, want %v", kinds, wantKinds)
	}
	if spawns != 1 {
		t.Fatalf("spawned %d retries, want 1", spawns)
	}
	if first.writes != 1 || second.writes != 1 {
		t.Fatalf("prompt written %d and %d times, want once per attempt", first.writes, second.writes)
	}
	if !first.closed {
		t.Fatal("the abandoned attempt was left open")
	}
	requireSessionDropped(t, r, second)
}
