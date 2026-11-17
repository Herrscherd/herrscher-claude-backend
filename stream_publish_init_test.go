package claude

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func awaitPublished(t *testing.T, published <-chan string, want string) {
	t.Helper()
	select {
	case got := <-published:
		if got != want {
			t.Fatalf("published id = %q while the turn is still running, want %q", got, want)
		}
	case <-time.After(returnDeadline):
		t.Fatalf("no id published within %s while the turn is still running, want %q: the id was not published when the CLI announced it", returnDeadline, want)
	}
}

func TestInitIDIsPublishedWhileTheTurnIsStillRunning(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	session := newStreamSession(discardWriteCloser{}, pr)
	published := make(chan string, 8)
	turnDone := make(chan error, 1)
	go func() {
		_, err := session.Send(context.Background(), "hi", nil, func(id string) {
			r.publishSessionID(id)
			published <- id
		})
		turnDone <- err
	}()

	mustWriteString(t, pw, sessionInitLine+"\n")
	awaitPublished(t, published, "sid-init")
	mustClose(t, pw)
	if err := awaitWithin(t, turnDone, "the turn to end"); err == nil {
		t.Fatal("the turn ended without its result line: want an error")
	}
	mustClose(t, r)
}

func TestTurnThatAnnouncedInitThenCrashesKeepsItsID(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = startShellSession(t, "read line; printf '%s\\n' '"+sessionInitLine+"' '"+sessionPartialLine+"'; exit 1")
	r.spawn = func(id string) (*streamSession, error) {
		t.Errorf("a turn the CLI took must not restart on its own (resume %q)", id)
		return nil, io.ErrClosedPipe
	}

	var duringTurn string
	onEvent := func(e contracts.BackendEvent) {
		if e.Kind == "text" {
			duringTurn = r.ResumeToken()
		}
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, onEvent); err == nil {
		t.Fatal("the crashed turn returned no error")
	}
	if duringTurn != "sid-init" {
		t.Fatalf("ResumeToken() during the turn = %q, want %q: the id must be published before the crash, not at the end of the turn", duringTurn, "sid-init")
	}
	if got := r.ResumeToken(); got != "sid-init" {
		t.Fatalf("ResumeToken() = %q after the crash, want %q", got, "sid-init")
	}
	mustClose(t, r)
}

func TestTurnCancelledAfterInitKeepsItsID(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(discardWriteCloser{}, pr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mustWriteString(t, pw, sessionInitLine+"\n"+sessionPartialLine+"\n")
	var duringTurn string
	onEvent := func(contracts.BackendEvent) {
		duringTurn = r.ResumeToken()
		cancel()
	}
	if _, err := respondWithin(ctx, t, r, contracts.Prompt{Content: "hi"}, onEvent); err == nil {
		t.Fatal("the cancelled turn returned no error")
	}
	if duringTurn != "sid-init" {
		t.Fatalf("ResumeToken() during the turn = %q, want %q: the id must be published before the cancel, not at the end of the turn", duringTurn, "sid-init")
	}
	if got := r.ResumeToken(); got != "sid-init" {
		t.Fatalf("ResumeToken() = %q after the cancel, want %q", got, "sid-init")
	}
	mustClose(t, pw)
}

func TestDaemonStopDuringTurnKeepsTheInitID(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(discardWriteCloser{}, pr)

	onEvent := func(contracts.BackendEvent) {
		if err := r.Close(); err != nil {
			t.Errorf("Close during the turn: %v", err)
		}
	}
	go mustWriteString(t, pw, sessionInitLine+"\n"+sessionPartialLine+"\n")
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "hi"}, onEvent); err == nil {
		t.Fatal("the turn Close interrupted returned no error")
	}
	if got := r.ResumeToken(); got != "sid-init" {
		t.Fatalf("ResumeToken() = %q after Close interrupted the turn, want %q: the id a stopped daemon must resume from", got, "sid-init")
	}
	mustClose(t, pw)
}

func TestResumedSessionAnnouncingANewIDRepublishesIt(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	r := newStreamResponder(context.Background(), nil, "", "sid-old", "", nil)
	var resumed []string
	r.spawn = func(id string) (*streamSession, error) {
		resumed = append(resumed, id)
		return newStreamSession(discardWriteCloser{}, pr), nil
	}

	midTurn := make(chan string, 1)
	turnDone := make(chan error, 1)
	go func() {
		_, err := r.Respond(context.Background(), contracts.Prompt{Content: "again"}, func(event contracts.BackendEvent) {
			if event.Kind == "text" {
				select {
				case midTurn <- r.ResumeToken():
				default:
				}
			}
		})
		turnDone <- err
	}()
	mustWriteString(t, pw, `{"type":"system","subtype":"init","session_id":"sid-new"}`+"\n"+
		`{"type":"assistant","session_id":"sid-new","message":{"content":[{"type":"text","text":"partial"}]}}`+"\n")
	awaitPublished(t, midTurn, "sid-new")
	mustClose(t, pw)
	if err := awaitWithin(t, turnDone, "the turn to end"); err == nil {
		t.Fatal("the turn ended without its result line: want an error")
	}
	if len(resumed) != 1 || resumed[0] != "sid-old" {
		t.Fatalf("spawned with resume ids %q, want [sid-old]: a turn that already took a line must not be relaunched", resumed)
	}
	mustClose(t, r)
}

func TestTurnPublishesEachIDChangeOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lines string
		want  []string
	}{
		{"an id repeated on later lines is published once", sessionInitLine + "\n" + sessionCarryingLine + "\n" + sessionResultWithoutIDLine, []string{"sid-init"}},
		{"a changed id is published again", sessionIDTextLine + "\n" + sessionLaterIDTextLine + "\n" + sessionResultWithoutIDLine, []string{"sid-x", "sid-z"}},
		{"an id that returns after another is published again", sessionIDTextLine + "\n" + sessionLaterIDTextLine + "\n" + sessionIDTextLine + "\n" + sessionResultWithoutIDLine, []string{"sid-x", "sid-z", "sid-x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var published []string
			turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(tc.lines), nil), nil, nil, func(id string) {
				published = append(published, id)
			})
			if err != nil {
				t.Fatalf("readTurn: %v", err)
			}
			if turn.Text != "done" {
				t.Fatalf("turn text = %q, want %q", turn.Text, "done")
			}
			if strings.Join(published, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("published ids = %q, want %q", published, tc.want)
			}
		})
	}
}
