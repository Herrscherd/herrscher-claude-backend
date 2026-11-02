package claude

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestAssistantLineEmitsUsageTokens(t *testing.T) {
	var events []contracts.BackendEvent
	collect := func(e contracts.BackendEvent) { events = append(events, e) }
	p := newTurnParser()

	assistant := []byte(`{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":42},"content":[{"type":"text","text":"hi"}]}}`)
	if _, done, err := p.parseLine(assistant, collect); err != nil || done {
		t.Fatal("assistant line must not terminate the turn")
	}

	usageEv := lastEventOfKind(events, "usage")
	if usageEv == nil {
		t.Fatal("expected a usage BackendEvent from the assistant line")
	}
	if usageEv.OutTokens != 42 || usageEv.InTokens != 10 {
		t.Fatalf("usage tokens: got in=%d out=%d, want in=10 out=42", usageEv.InTokens, usageEv.OutTokens)
	}
}

func TestResultLineCarriesUsageTokens(t *testing.T) {
	var events []contracts.BackendEvent
	collect := func(e contracts.BackendEvent) { events = append(events, e) }
	p := newTurnParser()

	result := []byte(`{"type":"result","session_id":"s1","result":"done","total_cost_usd":0.0042,"usage":{"input_tokens":100,"output_tokens":250}}`)
	tr, done, err := p.parseLine(result, collect)
	if err != nil {
		t.Fatalf("result line must decode: %v", err)
	}
	if !done {
		t.Fatal("result line must terminate the turn")
	}
	if tr.OutTokens != 250 || tr.InTokens != 100 {
		t.Fatalf("turnResult tokens: got in=%d out=%d, want in=100 out=250", tr.InTokens, tr.OutTokens)
	}
	resultEv := lastEventOfKind(events, "result")
	if resultEv == nil || resultEv.OutTokens != 250 {
		t.Fatalf("expected result BackendEvent with OutTokens=250, got %+v", resultEv)
	}
}

func lastEventOfKind(events []contracts.BackendEvent, kind string) *contracts.BackendEvent {
	var found *contracts.BackendEvent
	for i := range events {
		if events[i].Kind == kind {
			found = &events[i]
		}
	}
	return found
}
