package claude

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
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

type sleepingCLI struct {
	spawns  atomic.Int32
	started chan struct{}
	cmd     *exec.Cmd
	t       *testing.T
}

func (c *sleepingCLI) spawn(string) (*streamSession, error) {
	c.spawns.Add(1)
	cmd := exec.CommandContext(c.t.Context(), "sh", "-c", "exec sleep 60")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.cmd = cmd
	session := newStreamSession(stdin, stdout)
	session.cmd = cmd
	select {
	case c.started <- struct{}{}:
	default:
	}
	return session, nil
}

func awaitWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5 seconds", what)
	}
	var zero T
	return zero
}

func TestCloseInterruptsATurnBlockedOnTheCLI(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{started: make(chan struct{}, 1), t: t}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = cli.spawn

	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	awaitWithin(t, cli.started, "the CLI starting")

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	if err := awaitWithin(t, closed, "Close while a turn is blocked on the CLI"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := awaitWithin(t, responded, "Respond interrupted by Close"); !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("interrupted Respond err = %v, want ErrResponderClosed", err)
	}
	if n := cli.spawns.Load(); n != 1 {
		t.Fatalf("spawned %d processes, want 1: the interrupted turn must not be retried", n)
	}
	if cli.cmd.ProcessState == nil {
		t.Fatal("the CLI process was not killed and reaped by Close")
	}
}

func TestCloseFromResetCallbackDoesNotRespawn(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	var spawns atomic.Int32
	r.spawn = func(string) (*streamSession, error) {
		spawns.Add(1)
		return newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine)), nil
	}
	r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine))
	var closeErr error
	onEvent := func(e contracts.BackendEvent) {
		if e.Kind == "reset" {
			closeErr = r.Close()
		}
	}
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, onEvent)
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed", err)
	}
	if n := spawns.Load(); n != 0 {
		t.Fatalf("spawned %d processes after Close during retry, want 0", n)
	}
}

func TestCloseFromATextEventDoesNotRetry(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a turn closed mid-stream must not spawn a process to retry")
		return nil, errors.New("unexpected spawn")
	}
	r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine))
	resets := 0
	var closeErr error
	onEvent := func(e contracts.BackendEvent) {
		switch e.Kind {
		case "text":
			closeErr = r.Close()
		case "reset":
			resets++
		}
	}
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, onEvent)
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed", err)
	}
	if resets != 0 {
		t.Fatalf("reset emitted %d times after Close, want 0", resets)
	}
}

func TestCloseWaitsForASpawnInFlightToBeReaped(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{started: make(chan struct{}, 1), t: t}
	spawning := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(id string) (*streamSession, error) {
		close(spawning)
		<-release
		return cli.spawn(id)
	}

	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	awaitWithin(t, spawning, "the CLI spawn starting")

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while the spawn in flight was not yet reaped", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce()
	if err := awaitWithin(t, closed, "Close once the spawn in flight is reaped"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cli.cmd == nil || cli.cmd.ProcessState == nil {
		t.Fatal("Close returned before the process spawned after it was killed and reaped")
	}
	if err := awaitWithin(t, responded, "Respond whose spawn lost the race to Close"); !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed", err)
	}
}

func TestCloseGivesUpOnASpawnThatNeverReturns(t *testing.T) {
	t.Parallel()
	spawning := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		close(spawning)
		<-release
		return nil, errors.New("spawn released after Close gave up")
	}
	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	awaitWithin(t, spawning, "the CLI spawn starting")

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("Close returned nil while a spawn had not returned and so was not reaped")
		}
	case <-time.After(teardownBound + 5*time.Second):
		t.Fatal("Close did not give up within its bound while a spawn never returned")
	}
}

type blockingCloser struct {
	io.WriteCloser

	entered chan struct{}
	release <-chan struct{}
}

func (c blockingCloser) Close() error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	return c.WriteCloser.Close()
}

func TestConcurrentClosesReturnOnlyAfterTheProcessIsReaped(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{started: make(chan struct{}, 1), t: t}
	session, err := cli.spawn("")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	entered := make(chan struct{}, 1)
	session.stdin = blockingCloser{WriteCloser: session.stdin, entered: entered, release: release}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = session

	first := make(chan error, 1)
	go func() { first <- r.Close() }()
	awaitWithin(t, entered, "the first Close reaching the CLI")

	second := make(chan error, 1)
	go func() { second <- r.Close() }()
	select {
	case err := <-second:
		t.Fatalf("second Close returned (%v) before the process was reaped", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce()
	for _, closed := range []chan error{first, second} {
		if err := awaitWithin(t, closed, "a Close once the process is reaped"); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if cli.cmd.ProcessState == nil {
		t.Fatal("the CLI process was not reaped when the second Close returned")
	}
}

func TestInstallKillsTheSessionItReplaces(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	replaced := &spyWriteCloser{}
	r.sess = newStreamSession(replaced, strings.NewReader(""))
	if !r.install(newStreamSession(&spyWriteCloser{}, strings.NewReader(""))) {
		t.Fatal("install refused on an open responder")
	}
	if !replaced.closed {
		t.Fatal("the session that install replaced was not closed")
	}
}

func TestClosedResponderNamesItselfInTheSessionLanguage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lang i18n.Language
		want i18n.Language
	}{
		{"unknown language", i18n.Language(0), i18n.English},
		{"english", i18n.English, i18n.English},
		{"french", i18n.French, i18n.French},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			if err := r.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			want := mustRender(t, tc.want, keyTurnResponderClosed)
			assertRespondNamesClosedSession(t, r, tc.lang, want)
			assertCompactNamesClosedSession(t, r, tc.lang, want)
		})
	}
}

func assertRespondNamesClosedSession(t *testing.T, r *streamResponder, lang i18n.Language, want string) {
	t.Helper()
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi", Language: lang}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Respond: errors.Is(err, ErrResponderClosed) = false for %v", err)
	}
	if err.Error() != want {
		t.Fatalf("Respond error = %q, want the catalog text %q", err.Error(), want)
	}
}

func assertCompactNamesClosedSession(t *testing.T, r *streamResponder, lang i18n.Language, want string) {
	t.Helper()
	_, err := r.Compact(context.Background(), contracts.CompactRequest{Session: contracts.SessionInfo{Language: lang}}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Compact: errors.Is(err, ErrResponderClosed) = false for %v", err)
	}
	if !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("Compact error = %q, want it to end with the catalog text %q", err.Error(), want)
	}
}
