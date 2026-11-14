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
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

type sentLines struct{ bytes.Buffer }

func (s *sentLines) Close() error { return nil }

func compactingResponder(ctx context.Context, reply string) (*streamResponder, *sentLines) {
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
			r, sent := compactingResponder(t.Context(), compactSuccessWithUsage)
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
			r, _ := compactingResponder(t.Context(), tc.reply)
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
	r, _ := compactingResponder(t.Context(), reply)
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("err = %v, want it to match ErrTurnFailed", err)
	}
}

func TestRespondReportsATurnError(t *testing.T) {
	reply := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","session_id":"s"}` + "\n"
	r, _ := compactingResponder(t.Context(), reply)
	_, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi"}, nil)
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("err = %v, want it to match ErrTurnFailed", err)
	}
}

func TestCompactPublishesTheSessionID(t *testing.T) {
	reply := `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"sess-after-compact"}` + "\n"
	r, _ := compactingResponder(t.Context(), reply)
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
		r, sent := compactingResponder(ctx, compactSuccessWithUsage)
		if _, err := r.Compact(ctx, contracts.CompactRequest{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Compact err = %v, want context.Canceled", err)
		}
		if sent.Len() != 0 {
			t.Fatalf("Compact sent %q under a cancelled context", sent.String())
		}
	})
	t.Run("respond", func(t *testing.T) {
		r, sent := compactingResponder(ctx, compactSuccessWithUsage)
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

	compactErr := make(chan error, 1)
	respondErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
		compactErr <- err
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Compact never reached spawn")
	}
	wg.Go(func() {
		_, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi"}, nil)
		respondErr <- err
	})
	wg.Wait()
	if err := <-compactErr; err != nil {
		t.Errorf("Compact: %v", err)
	}
	if err := <-respondErr; err != nil {
		t.Errorf("Respond: %v", err)
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("spawned %d sessions, want 1: Compact and Respond ran in parallel on the same responder", got)
	}
}

func TestCompactForwardsProgressEvents(t *testing.T) {
	r, _ := compactingResponder(t.Context(), compactSuccessWithUsage)
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

func TestCompactReportsItsSuccessAsACode(t *testing.T) {
	r, _ := compactingResponder(t.Context(), compactSuccessWithUsage)
	got, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got.Notice.Code != contracts.CodeCompacted {
		t.Fatalf("Notice.Code = %v, want CodeCompacted", got.Notice.Code)
	}
	if len(got.Notice.Args) != 0 {
		t.Fatalf("Notice.Args = %+v, want none", got.Notice.Args)
	}
}

func TestCompactNoteIsEnglish(t *testing.T) {
	r, _ := compactingResponder(t.Context(), compactSuccessWithUsage)
	got, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	const want = "Context compacted, its size after compaction is measured on the next turn."
	if got.Note != want {
		t.Fatalf("Note = %q, want %q", got.Note, want)
	}
}

func resultLine(t *testing.T, text string) string {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	return `{"type":"result","subtype":"error_during_execution","is_error":true,"result":` + string(encoded) + `,"session_id":"s"}` + "\n"
}

func TestCompactUnavailableCarriesItsReasonAsACode(t *testing.T) {
	oversized := strings.Repeat("x", 200000) + "\nsecond line"
	cases := []struct {
		name       string
		cancelled  bool
		reply      string
		is         error
		wantReason string
		keeps      string
	}{
		{name: "turn reports an error", reply: resultLine(t, "boom"), is: ErrTurnFailed, wantReason: "boom"},
		{name: "context cancelled", cancelled: true, reply: compactSuccessWithUsage, is: context.Canceled, wantReason: "context canceled"},
		{name: "reason is the first non blank line", reply: resultLine(t, "\n  line one  \nline two"), is: ErrTurnFailed, wantReason: "line one", keeps: "line two"},
		{name: "oversized multi line cause is cut to 200 runes", reply: resultLine(t, oversized), is: ErrTurnFailed, wantReason: strings.Repeat("x", 199) + "…", keeps: "second line"},
		{name: "empty result falls back to the marked API error text", reply: apiErrorMessageLine(syntheticAPIErrorText) + "\n" + resultLine(t, ""), is: ErrTurnFailed, wantReason: syntheticAPIErrorText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.cancelled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			r, _ := compactingResponder(ctx, tc.reply)
			_, err := r.Compact(ctx, contracts.CompactRequest{}, nil)
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want it to match %v", err, tc.is)
			}
			assertUnavailableReason(t, err, tc.wantReason)
			if tc.keeps != "" && !strings.Contains(err.Error(), tc.keeps) {
				t.Fatalf("Error() lost the full cause %q: %q", tc.keeps, err.Error())
			}
		})
	}
}

func TestCompactUnavailableReasonOfASpawnFailure(t *testing.T) {
	const binary = "/nonexistent/claude-binary-for-test"
	r := newStreamResponder(t.Context(), []string{binary}, "", "", "", nil)
	_, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil)
	if err == nil {
		t.Fatal("Compact with a missing binary succeeded")
	}
	assertUnavailableReason(t, err, "claude stream: start: fork/exec "+binary+": no such file or directory")
}

func assertUnavailableReason(t *testing.T, err error, wantReason string) {
	t.Helper()
	notice := contracts.NoticeOf(err)
	if notice.Code != contracts.CodeCompactUnavailable {
		t.Fatalf("Notice.Code = %v, want CodeCompactUnavailable", notice.Code)
	}
	if len(notice.Args) != 1 || notice.Args[0].Name() != "Reason" || notice.Args[0].Kind() != contracts.ArgText {
		t.Fatalf("Notice.Args = %+v, want one text argument named Reason", notice.Args)
	}
	if notice.Args[0].Value() != wantReason {
		t.Fatalf("Reason = %q, want %q", notice.Args[0].Value(), wantReason)
	}
	text, renderErr := contracts.NewRenderer().Render(i18n.English, notice)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if !strings.Contains(text, wantReason) {
		t.Fatalf("rendered %q does not show the reason %q", text, wantReason)
	}
}
