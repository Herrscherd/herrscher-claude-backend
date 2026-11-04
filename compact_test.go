package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type sentLines struct{ bytes.Buffer }

func (s *sentLines) Close() error { return nil }

func compactingResponder(t *testing.T, reply string) (*streamResponder, *sentLines) {
	t.Helper()
	return compactingResponderWith(t.Context(), t, reply)
}

func compactingResponderWith(ctx context.Context, t *testing.T, reply string) (*streamResponder, *sentLines) {
	t.Helper()
	sent := &sentLines{}
	r := newStreamResponder(ctx, nil, "", "", "", nil)
	r.sess = newStreamSession(sent, strings.NewReader(reply))
	return r, sent
}

func sentMessage(t *testing.T, sent *sentLines) string {
	t.Helper()
	var line struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(sent.Bytes()), &line); err != nil {
		t.Fatalf("sent line is not a user message: %v: %q", err, sent.String())
	}
	return line.Message.Content
}

const compactSuccessWithUsage = `{"type":"assistant","message":{"content":[{"type":"text","text":"je compacte"}]},"session_id":"s"}
{"type":"result","subtype":"success","is_error":false,"result":"Compacté","total_cost_usd":0.01,"session_id":"s","usage":{"input_tokens":30,"output_tokens":55,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}}
`

func TestCompactSendsTheCommandAsATurn(t *testing.T) {
	cases := []struct {
		name        string
		instruction string
		want        string
	}{
		{name: "no instruction", instruction: "", want: "/compact"},
		{name: "blank instruction", instruction: "   ", want: "/compact"},
		{name: "instruction", instruction: "garde les décisions", want: "/compact garde les décisions"},
		{name: "instruction on several lines", instruction: "garde\nles décisions", want: "/compact garde\nles décisions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, sent := compactingResponder(t, compactSuccessWithUsage)
			if _, err := r.Compact(t.Context(), contracts.CompactRequest{Instruction: tc.instruction}, nil); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if got := sentMessage(t, sent); got != tc.want {
				t.Fatalf("sent %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompactNeverClaimsTheSizeAfterCompaction(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{name: "turn reports usage", reply: compactSuccessWithUsage},
		{name: "turn reports no usage", reply: `{"type":"result","subtype":"success","is_error":false,"result":"Compacté","session_id":"s"}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := compactingResponder(t, tc.reply)
			got, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if got.TokensMeasured || got.TokensAfter != 0 {
				t.Fatalf("TokensAfter = %d measured %t, want 0 not measured", got.TokensAfter, got.TokensMeasured)
			}
			if got.Done {
				t.Fatal("the backend must not stop the stack")
			}
			if got.Note == "" {
				t.Fatal("expected a note for the operator")
			}
		})
	}
}

func TestCompactWrapsATurnError(t *testing.T) {
	reply := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","session_id":"s"}` + "\n"
	r, _ := compactingResponder(t, reply)
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("err = %v, want it to match ErrTurnFailed", err)
	}
}

func TestRespondReportsATurnError(t *testing.T) {
	reply := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","session_id":"s"}` + "\n"
	r, _ := compactingResponder(t, reply)
	_, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi"}, nil)
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("err = %v, want it to match ErrTurnFailed", err)
	}
}

func TestCompactPublishesTheSessionID(t *testing.T) {
	reply := `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"sess-after-compact"}` + "\n"
	r, _ := compactingResponder(t, reply)
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := r.currentSessionID(); got != "sess-after-compact" {
		t.Fatalf("session id = %q, want sess-after-compact", got)
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	t.Run("compact", func(t *testing.T) {
		r, sent := compactingResponderWith(ctx, t, compactSuccessWithUsage)
		if _, err := r.Compact(ctx, contracts.CompactRequest{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Compact err = %v, want context.Canceled", err)
		}
		if sent.Len() != 0 {
			t.Fatalf("Compact sent %q under a cancelled context", sent.String())
		}
	})
	t.Run("respond", func(t *testing.T) {
		r, sent := compactingResponderWith(ctx, t, compactSuccessWithUsage)
		if _, err := r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Respond err = %v, want context.Canceled", err)
		}
		if sent.Len() != 0 {
			t.Fatalf("Respond sent %q under a cancelled context", sent.String())
		}
	})
}

func TestCompactAndRespondNeverOverlap(t *testing.T) {
	const turnReply = `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	var spawns atomic.Int32
	overlapped := make(chan struct{})
	entered := make(chan struct{}, 1)
	var overlapOnce sync.Once
	r := newStreamResponder(t.Context(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		if spawns.Add(1) > 1 {
			overlapOnce.Do(func() { close(overlapped) })
			return newStreamSession(&sentLines{}, strings.NewReader(turnReply+turnReply)), nil
		}
		entered <- struct{}{}
		select {
		case <-overlapped:
		case <-time.After(500 * time.Millisecond):
		}
		return newStreamSession(&sentLines{}, strings.NewReader(turnReply+turnReply)), nil
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
			t.Errorf("Compact: %v", err)
		}
	})
	<-entered
	wg.Go(func() {
		if _, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi"}, nil); err != nil {
			t.Errorf("Respond: %v", err)
		}
	})
	wg.Wait()
	if got := spawns.Load(); got != 1 {
		t.Fatalf("spawned %d sessions, want 1: Compact and Respond ran in parallel on the same responder", got)
	}
}

func TestCompactForwardsProgressEvents(t *testing.T) {
	r, _ := compactingResponder(t, compactSuccessWithUsage)
	var got []contracts.BackendEvent
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, func(e contracts.BackendEvent) { got = append(got, e) }); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	for _, e := range got {
		if e.Kind == "text" && e.Detail == "je compacte" {
			return
		}
	}
	t.Fatalf("no text progress event in %+v", got)
}

func TestClaudeManifestDeclaresCompactor(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != backendKind {
			continue
		}
		if !p.Manifest.Capabilities.Compactor {
			t.Fatal("the claude manifest must declare Capabilities.Compactor")
		}
		return
	}
	t.Fatalf("no %q backend registered", backendKind)
}
