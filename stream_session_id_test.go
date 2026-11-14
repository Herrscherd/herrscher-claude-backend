package claude

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	sessionInitLine            = `{"type":"system","subtype":"init","session_id":"sid-init"}`
	sessionPartialLine         = `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionCarryingLine        = `{"type":"assistant","session_id":"sid-init","message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionResumedTurnEnd      = `{"type":"result","is_error":false,"result":"done","session_id":"sid-init"}`
	sessionIDTextLine          = `{"type":"assistant","session_id":"sid-x","message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionResultWithoutIDLine = `{"type":"result","is_error":false,"result":"done"}`
	sessionNumericIDTextLine   = `{"type":"assistant","session_id":42,"message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionResultOwnIDLine     = `{"type":"result","is_error":false,"result":"done","session_id":"sid-y"}`
)

func TestDeathAfterALineCarryingASessionIDResumesThatSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lines string
	}{
		{"init line then text", sessionInitLine + "\n" + sessionPartialLine},
		{"text line carrying the id", sessionCarryingLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "prompts.log")
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			r.sess = startShellSession(t, loggedPromptCLI(log, tc.lines))
			var resumed []string
			r.spawn = func(id string) (*streamSession, error) {
				resumed = append(resumed, id)
				return startShellSession(t, loggedPromptCLI(log, sessionResumedTurnEnd)), nil
			}

			if _, err := r.Respond(context.Background(), contracts.Prompt{Content: "first"}, nil); !errors.Is(err, io.EOF) {
				t.Fatalf("first turn err = %v, want the stream-ended error wrapping io.EOF", err)
			}
			reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "second"}, nil)
			if err != nil {
				t.Fatalf("second turn: %v", err)
			}
			if reply != "done" {
				t.Fatalf("second turn reply = %q, want %q", reply, "done")
			}
			if !slices.Equal(resumed, []string{"sid-init"}) {
				t.Fatalf("restarted with resume ids %q, want [sid-init]", resumed)
			}
			mustClose(t, r)
		})
	}
}

func TestInitLineAloneCountsAsATakenTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, sessionInitLine))
	var resumed []string
	r.spawn = func(id string) (*streamSession, error) {
		resumed = append(resumed, id)
		return startShellSession(t, loggedPromptCLI(log, sessionResumedTurnEnd)), nil
	}

	var kinds []string
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "first"}, func(e contracts.BackendEvent) {
		kinds = append(kinds, e.Kind)
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("first turn err = %v, want the stream-ended error wrapping io.EOF", err)
	}
	if n := promptsReceived(t, log); n != 1 {
		t.Fatalf("the prompt reached the CLI %d times, want 1: the init line means the CLI took it", n)
	}
	if slices.Contains(kinds, "reset") {
		t.Fatalf("events = %q, want no reset for a turn the CLI took", kinds)
	}
	reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "second"}, nil)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if reply != "done" {
		t.Fatalf("second turn reply = %q, want %q", reply, "done")
	}
	if !slices.Equal(resumed, []string{"sid-init"}) {
		t.Fatalf("restarted with resume ids %q, want [sid-init]", resumed)
	}
	mustClose(t, r)
}

func TestTurnEndingWithoutSessionIDKeepsTheIDOfItsLines(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, sessionIDTextLine+"\n"+sessionResultWithoutIDLine))
	r.spawn = func(id string) (*streamSession, error) {
		t.Errorf("a successful turn must not restart the session (resume %q)", id)
		return nil, errors.New("unexpected spawn")
	}

	reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	if got := r.ResumeToken(); got != "sid-x" {
		t.Fatalf("ResumeToken() = %q, want %q: the id of the lines before the result", got, "sid-x")
	}
	mustClose(t, r)
}

func TestResultSessionIDWinsOverTheIDOfItsEarlierLines(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, sessionIDTextLine+"\n"+sessionResultOwnIDLine))
	r.spawn = func(id string) (*streamSession, error) {
		t.Errorf("a successful turn must not restart the session (resume %q)", id)
		return nil, errors.New("unexpected spawn")
	}

	reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	if got := r.ResumeToken(); got != "sid-y" {
		t.Fatalf("ResumeToken() = %q, want %q: the result's id replaces the id of the lines before it", got, "sid-y")
	}
	mustClose(t, r)
}

func TestNonStringSessionIDOnANonResultLineIsIgnored(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, sessionNumericIDTextLine+"\n"+sessionResultWithoutIDLine))
	r.spawn = func(id string) (*streamSession, error) {
		t.Errorf("a successful turn must not restart the session (resume %q)", id)
		return nil, errors.New("unexpected spawn")
	}

	reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	if got := r.ResumeToken(); got != "conv-1" {
		t.Fatalf("ResumeToken() = %q, want %q: a non-string session_id is not recorded", got, "conv-1")
	}
	mustClose(t, r)
}
