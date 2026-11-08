package claude

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestResultLineCarriesPerModelUsage(t *testing.T) {
	var events []contracts.BackendEvent
	collect := func(e contracts.BackendEvent) { events = append(events, e) }
	p := newTurnParser()

	result := []byte(`{"type":"result","session_id":"s1","result":"done","total_cost_usd":0.0042,"usage":{"input_tokens":100,"output_tokens":250},` +
		`"modelUsage":{` +
		`"claude-opus-5-5":{"inputTokens":90,"outputTokens":200,"cacheReadInputTokens":5,"cacheCreationInputTokens":7,"costUSD":0.003},` +
		`"claude-haiku-5-5":{"inputTokens":10,"outputTokens":50,"cacheReadInputTokens":0,"cacheCreationInputTokens":2,"costUSD":0.0012}}}`)
	if _, done, err := p.parseLine(result, collect); err != nil || !done {
		t.Fatalf("result line must decode and end the turn: done=%v err=%v", done, err)
	}

	resultEv := lastEventOfKind(events, "result")
	if resultEv == nil {
		t.Fatal("expected a result BackendEvent")
	}
	want := []contracts.ModelUsage{
		{Model: "claude-haiku-5-5", In: 10, Out: 50, CacheRead: 0, CacheCreate: 2, Cost: 0.0012},
		{Model: "claude-opus-5-5", In: 90, Out: 200, CacheRead: 5, CacheCreate: 7, Cost: 0.003},
	}
	if len(resultEv.Models) != len(want) {
		t.Fatalf("models = %+v, want %+v", resultEv.Models, want)
	}
	for i := range want {
		if resultEv.Models[i] != want[i] {
			t.Fatalf("models[%d] = %+v, want %+v", i, resultEv.Models[i], want[i])
		}
	}
}

func TestResultLineWithoutModelUsageHasNoModels(t *testing.T) {
	var events []contracts.BackendEvent
	collect := func(e contracts.BackendEvent) { events = append(events, e) }
	p := newTurnParser()

	result := []byte(`{"type":"result","session_id":"s1","result":"done","total_cost_usd":0.0042}`)
	if _, _, err := p.parseLine(result, collect); err != nil {
		t.Fatalf("result line must decode: %v", err)
	}
	if resultEv := lastEventOfKind(events, "result"); resultEv == nil || resultEv.Models != nil {
		t.Fatalf("expected no models, got %+v", resultEv)
	}
}
