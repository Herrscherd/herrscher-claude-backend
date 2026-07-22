package claude

import (
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

// TestParseTurnLine_UsageEmitsTokens asserts that token usage is parsed from
// assistant lines (live) and the terminal result line (final) and surfaced on
// BackendEvents and the turnResult.
func TestParseTurnLine_UsageEmitsTokens(t *testing.T) {
	var events []contracts.BackendEvent
	collect := func(e contracts.BackendEvent) { events = append(events, e) }

	assistant := []byte(`{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":42},"content":[{"type":"text","text":"hi"}]}}`)
	if _, done := parseTurnLine(assistant, collect); done {
		t.Fatal("assistant line must not terminate the turn")
	}

	var usageEv *contracts.BackendEvent
	for i := range events {
		if events[i].Kind == "usage" {
			usageEv = &events[i]
		}
	}
	if usageEv == nil {
		t.Fatal("expected a usage BackendEvent from the assistant line")
	}
	if usageEv.OutTokens != 42 || usageEv.InTokens != 10 {
		t.Fatalf("usage tokens: got in=%d out=%d, want in=10 out=42", usageEv.InTokens, usageEv.OutTokens)
	}

	events = nil
	result := []byte(`{"type":"result","session_id":"s1","result":"done","total_cost_usd":0.0042,"usage":{"input_tokens":100,"output_tokens":250}}`)
	tr, done := parseTurnLine(result, collect)
	if !done {
		t.Fatal("result line must terminate the turn")
	}
	if tr.OutTokens != 250 || tr.InTokens != 100 {
		t.Fatalf("turnResult tokens: got in=%d out=%d, want in=100 out=250", tr.InTokens, tr.OutTokens)
	}
	var resultEv *contracts.BackendEvent
	for i := range events {
		if events[i].Kind == "result" {
			resultEv = &events[i]
		}
	}
	if resultEv == nil || resultEv.OutTokens != 250 {
		t.Fatalf("expected result BackendEvent with OutTokens=250, got %+v", resultEv)
	}
}
