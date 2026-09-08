package claude

import (
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func collectLines(t *testing.T, lines ...string) []contracts.BackendEvent {
	t.Helper()
	var got []contracts.BackendEvent
	p := newTurnParser()
	for _, ln := range lines {
		if _, _, err := p.parseLine([]byte(ln), func(e contracts.BackendEvent) { got = append(got, e) }); err != nil {
			t.Fatalf("parseLine(%s): %v", ln, err)
		}
	}
	return got
}

func TestTodoWriteBecomesATodosEvent(t *testing.T) {
	got := collectLines(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"TodoWrite","input":{"todos":[{"content":"lire","status":"completed"},{"content":"ecrire","status":"in_progress"},{"content":"tester","status":"pending"}]}}]}}`)
	if len(got) != 1 || got[0].Kind != "todos" {
		t.Fatalf("attendu un seul evenement todos, obtenu %+v", got)
	}
	want := []contracts.TodoItem{{Text: "lire", State: "done"}, {Text: "ecrire", State: "active"}, {Text: "tester", State: "pending"}}
	if len(got[0].Todos) != len(want) {
		t.Fatalf("todos: %+v", got[0].Todos)
	}
	for i, w := range want {
		if got[0].Todos[i] != w {
			t.Fatalf("todo %d: got %+v, want %+v", i, got[0].Todos[i], w)
		}
	}
}

func TestTaskOpensAndClosesASubagent(t *testing.T) {
	got := collectLines(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a1","name":"Task","input":{"description":"chercher le bug","subagent_type":"Explore"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a1"}]}}`,
	)
	if len(got) != 2 {
		t.Fatalf("attendu deux evenements, obtenu %+v", got)
	}
	start := got[0].Subagent
	if got[0].Kind != "subagent" || start == nil || start.ID != "a1" || start.Name != "chercher le bug" || start.Kind != "Explore" || start.State != "active" {
		t.Fatalf("ouverture: %+v", got[0])
	}
	end := got[1].Subagent
	if got[1].Kind != "subagent" || end == nil || end.ID != "a1" || end.State != "done" {
		t.Fatalf("fermeture: %+v", got[1])
	}
}

func TestTaskFallsBackToItsType(t *testing.T) {
	got := collectLines(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a1","name":"Task","input":{"subagent_type":"Explore"}}]}}`)
	if len(got) != 1 || got[0].Subagent.Name != "Explore" {
		t.Fatalf("nom de repli: %+v", got)
	}
}

func TestToolResultOfAnOrdinaryToolIsIgnored(t *testing.T) {
	got := collectLines(t, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b1"}]}}`)
	if len(got) != 0 {
		t.Fatalf("attendu aucun evenement, obtenu %+v", got)
	}
}

func TestOrdinaryToolsStayInTheTranscript(t *testing.T) {
	got := collectLines(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b1","name":"Read","input":{"file_path":"/x"}}]}}`)
	if len(got) != 1 || got[0].Kind != "tool" || got[0].Tool != "Read" || got[0].Detail != "/x" {
		t.Fatalf("outil ordinaire: %+v", got)
	}
}
