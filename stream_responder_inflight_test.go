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
)

const (
	sleepScript           = `read line; exec sleep 60`
	resultThenSleepScript = `read line; printf '%s\n' '{"type":"result","is_error":false,"result":"done","session_id":"s"}'; exec sleep 60`
)

type sleepingCLI struct {
	spawns   atomic.Int32
	prompted chan struct{}
	cmd      *exec.Cmd
	script   string
	t        *testing.T
}

type promptWriteCloser struct {
	io.WriteCloser

	prompted chan<- struct{}
}

func (w promptWriteCloser) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err == nil {
		select {
		case w.prompted <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (c *sleepingCLI) spawn(string) (*streamSession, error) {
	c.spawns.Add(1)
	cmd := newGroupCommand(c.t.Context(), "sh", "-c", c.script)
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
	session := newStreamSession(promptWriteCloser{WriteCloser: stdin, prompted: c.prompted}, stdout)
	session.cmd = cmd
	return session, nil
}

func TestCloseInterruptsATurnBlockedOnTheCLI(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{prompted: make(chan struct{}, 1), t: t, script: sleepScript}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = cli.spawn

	responded := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
		responded <- err
	}()
	awaitWithin(t, cli.prompted, "the prompt written to the CLI")
	select {
	case err := <-responded:
		t.Fatalf("Respond returned (%v) before Close: the turn was not blocked on the CLI", err)
	default:
	}

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
	r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	var closeErr error
	onEvent := func(e contracts.BackendEvent) {
		if e.Kind == "reset" {
			closeErr = returnWithin(t, "Close", r.Close)
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
			closeErr = returnWithin(t, "Close", r.Close)
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
	cli := &sleepingCLI{prompted: make(chan struct{}, 1), t: t, script: sleepScript}
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
	cli := &sleepingCLI{prompted: make(chan struct{}, 1), t: t, script: sleepScript}
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
