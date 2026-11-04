package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type sentLines struct{ bytes.Buffer }

func (s *sentLines) Close() error { return nil }

func compactingResponder(t *testing.T, reply string) (*streamResponder, *sentLines) {
	t.Helper()
	sent := &sentLines{}
	r := newStreamResponder(t.Context(), nil, "", "", "", nil)
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
