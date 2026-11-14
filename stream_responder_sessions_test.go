package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

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

func TestReplacedAndDroppedSessionsKeepThePendingCountBalanced(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	first := newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	second := newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	if !r.install(first) || !r.install(second) {
		t.Fatal("install refused on an open responder")
	}
	if err := r.dropSession(second); err != nil {
		t.Fatalf("dropSession: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2 seconds: pending no longer balances to zero")
	}
}

func TestDropSessionAfterCloseStillReapsItsProcess(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{prompted: make(chan struct{}, 1), t: t, script: sleepScript}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	session, err := cli.spawn("")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := r.dropSession(session); err != nil {
		t.Fatalf("dropSession after Close: %v", err)
	}
	if cli.cmd.ProcessState == nil {
		t.Fatal("dropSession after Close did not reap the process it was given")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close after dropSession: %v, want nil", err)
	}
}

func TestRespondAfterCloseWithCancelledContextReturnsErrResponderClosed(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a closed responder must not spawn a process")
		return nil, errors.New("unexpected spawn")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("err = %v, want ErrResponderClosed, not the context error", err)
	}
}

func TestCloseFromTheResultEventKeepsTheTurnAndKillsTheProcess(t *testing.T) {
	t.Parallel()
	cli := &sleepingCLI{t: t, script: resultThenSleepScript}
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = cli.spawn
	var closeErr error
	onEvent := func(e contracts.BackendEvent) {
		if e.Kind == "result" {
			closeErr = r.Close()
		}
	}
	text, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, onEvent)
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("err = %v, want nil: a turn whose result line was read is rendered whole", err)
	}
	if text != "done" {
		t.Fatalf("text = %q, want %q", text, "done")
	}
	if cli.cmd.ProcessState == nil {
		t.Fatal("the CLI process was not killed and reaped by Close")
	}
	_, err = r.Respond(context.Background(), contracts.Prompt{Content: "again"}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Respond after Close err = %v, want ErrResponderClosed", err)
	}
	if n := cli.spawns.Load(); n != 1 {
		t.Fatalf("spawned %d processes, want 1: Respond after Close must not spawn", n)
	}
}
