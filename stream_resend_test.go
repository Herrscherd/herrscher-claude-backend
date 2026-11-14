package claude

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const (
	resendPartialLine    = `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`
	resendThinkingLine   = `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`
	resendToolUseLine    = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	resendToolResultLine = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	resendUsageLine      = `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`
	resendAPIRetryLine   = `{"type":"system","subtype":"api_retry","attempt":1,"session_id":"conv-1"}`
	resendHookLine       = `{"type":"system","subtype":"hook_started"}`
	resendResultLine     = `{"type":"result","is_error":false,"result":"done","session_id":"conv-2"}`
)

func loggedPromptCLI(log, stdout string) string {
	script := "read line; printf '%s\\n' \"$line\" >> " + log
	if stdout != "" {
		script += "; printf '%s\\n' '" + stdout + "'"
	}
	return script
}

func promptsReceived(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read prompt log: %v", err)
	}
	return strings.Count(string(data), "\n")
}

func TestRespondDoesNotResendAPromptTheCLIAlreadyTook(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		event string
	}{
		{"text", resendPartialLine},
		{"thinking", resendThinkingLine},
		{"tool use", resendToolUseLine},
		{"tool result", resendToolResultLine},
		{"usage", resendUsageLine},
		{"api retry", resendAPIRetryLine},
		{"init", sessionInitLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "prompts.log")
			r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
			r.sess = startShellSession(t, loggedPromptCLI(log, tc.event))
			r.spawn = func(string) (*streamSession, error) {
				t.Error("a turn the CLI already took must not restart the session")
				return nil, errors.New("unexpected spawn")
			}

			_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want the stream-ended error wrapping io.EOF", err)
			}
			if n := promptsReceived(t, log); n != 1 {
				t.Fatalf("the CLI received the prompt %d times, want 1", n)
			}
			mustClose(t, r)
		})
	}
}

func TestRespondKeepsThePartialTextOfADeadTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, resendPartialLine))
	r.spawn = func(string) (*streamSession, error) {
		t.Error("a turn the CLI already took must not restart the session")
		return nil, errors.New("unexpected spawn")
	}

	var kinds, texts []string
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi", Language: i18n.English}, func(e contracts.BackendEvent) {
		kinds = append(kinds, e.Kind)
		if e.Kind == "text" {
			texts = append(texts, e.Detail)
		}
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want the stream-ended error wrapping io.EOF", err)
	}
	if want := []string{"text"}; !slices.Equal(kinds, want) {
		t.Fatalf("events = %q, want %q: no reset before the partial text", kinds, want)
	}
	assertTextDetails(t, texts, []string{"partial"})
	mustClose(t, r)
}

func TestRespondResendsThePromptOnceWhenTheCLIDiesBeforeAnyEvent(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, ""))
	var resumed []string
	r.spawn = func(id string) (*streamSession, error) {
		resumed = append(resumed, id)
		return startShellSession(t, loggedPromptCLI(log, resendResultLine)), nil
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
	if n := promptsReceived(t, log); n != 2 {
		t.Fatalf("the prompt reached the CLI %d times, want 2: one resend", n)
	}
	if !slices.Equal(resumed, []string{"conv-1"}) {
		t.Fatalf("restarted with resume ids %q, want [conv-1]", resumed)
	}
	if !slices.Contains(kinds, "reset") {
		t.Fatalf("events = %q, want a reset before the resend", kinds)
	}
	mustClose(t, r)
}

func TestHookLineAloneDoesNotCountAsATakenTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, resendHookLine))
	var resumed []string
	r.spawn = func(id string) (*streamSession, error) {
		resumed = append(resumed, id)
		return startShellSession(t, loggedPromptCLI(log, resendResultLine)), nil
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
	if n := promptsReceived(t, log); n != 2 {
		t.Fatalf("the prompt reached the CLI %d times, want 2: a hook line is not a turn the CLI took", n)
	}
	if !slices.Equal(resumed, []string{"conv-1"}) {
		t.Fatalf("restarted with resume ids %q, want [conv-1]", resumed)
	}
	if !slices.Contains(kinds, "reset") {
		t.Fatalf("events = %q, want a reset before the resend", kinds)
	}
	mustClose(t, r)
}

func TestTurnAfterADeathMidTurnResumesTheSession(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "prompts.log")
	r := newStreamResponder(context.Background(), nil, "", "conv-1", "", nil)
	r.sess = startShellSession(t, loggedPromptCLI(log, resendPartialLine))
	var resumed []string
	r.spawn = func(id string) (*streamSession, error) {
		resumed = append(resumed, id)
		return startShellSession(t, loggedPromptCLI(log, resendResultLine)), nil
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
	if !slices.Equal(resumed, []string{"conv-1"}) {
		t.Fatalf("restarted with resume ids %q, want [conv-1]", resumed)
	}
	if n := promptsReceived(t, log); n != 2 {
		t.Fatalf("the prompts reached the CLI %d times, want 2: one per turn", n)
	}
	mustClose(t, r)
}
