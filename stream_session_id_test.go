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
	sessionInitLine       = `{"type":"system","subtype":"init","session_id":"sid-init"}`
	sessionPartialLine    = `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionCarryingLine   = `{"type":"assistant","session_id":"sid-init","message":{"content":[{"type":"text","text":"partial"}]}}`
	sessionResumedTurnEnd = `{"type":"result","is_error":false,"result":"done","session_id":"sid-init"}`
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

func TestInitLineAloneDoesNotCountAsATakenTurn(t *testing.T) {
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
	reply, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, func(e contracts.BackendEvent) {
		kinds = append(kinds, e.Kind)
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	if !slices.Equal(resumed, []string{"sid-init"}) {
		t.Fatalf("restarted with resume ids %q, want [sid-init]", resumed)
	}
	if n := promptsReceived(t, log); n != 2 {
		t.Fatalf("the prompt reached the CLI %d times, want 2: one resend", n)
	}
	if !slices.Contains(kinds, "reset") {
		t.Fatalf("events = %q, want a reset before the resend", kinds)
	}
	mustClose(t, r)
}
