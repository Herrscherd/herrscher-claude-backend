package claude

import (
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	bashStartLine = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"go test ./..."}}]},"session_id":"s"}`
	bashOKLine    = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok  \tclaude\t0.4s"}]},"session_id":"s"}`
)

func TestToolCallStartCarriesTheToolUseID(t *testing.T) {
	got := collectLines(t, bashStartLine)
	if len(got) != 1 {
		t.Fatalf("attendu un evenement, obtenu %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindTool,
		Tool:     "Bash",
		Detail:   "go test ./...",
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", Detail: "go test ./...", State: contracts.ToolCallStarted},
	}
	if !sameEvent(t, got[0], want) {
		t.Fatalf("debut = %+v, want %+v", got[0], want)
	}
}

func TestSuccessfulToolResultClosesTheCall(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine)
	if len(got) != 2 {
		t.Fatalf("attendu deux evenements, obtenu %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[1], want) {
		t.Fatalf("fin = %+v, want %+v", got[1], want)
	}
}

func TestFailedToolResultCarriesItsReason(t *testing.T) {
	failure := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"exit status 1: missing go.sum entry","is_error":true}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, failure)
	if len(got) != 2 {
		t.Fatalf("attendu deux evenements, obtenu %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallFailed, Error: "exit status 1: missing go.sum entry"},
	}
	if !sameEvent(t, got[1], want) {
		t.Fatalf("echec = %+v, want %+v", got[1], want)
	}
}

func TestFailedToolResultReasonIsBoundedOnARuneBoundary(t *testing.T) {
	long := "a" + strings.Repeat("é", 300)
	failure := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"` + long + `","is_error":true}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, failure)
	if len(got) != 2 || got[1].ToolCall == nil {
		t.Fatalf("attendu un echec, obtenu %+v", got)
	}
	reason := got[1].ToolCall.Error
	if !utf8.ValidString(reason) {
		t.Fatalf("raison coupee au milieu d'un caractere: %q", reason)
	}
	if n := utf8.RuneCountInString(reason); n == 0 || n > maxToolErrorRunes {
		t.Fatalf("raison de %d caracteres, attendu entre 1 et %d", n, maxToolErrorRunes)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(reason, "…")) {
		t.Fatalf("raison %q n'est pas le debut du resultat", reason)
	}
}

func TestSecondResultOfACallEmitsNothing(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine, bashOKLine)
	if len(got) != 2 {
		t.Fatalf("un appel ne se ferme qu'une fois, obtenu %+v", got)
	}
}

func TestResultOfACallStartedBeforeAResetEmitsNothing(t *testing.T) {
	before := collectLines(t, bashStartLine)
	if len(before) != 1 {
		t.Fatalf("attendu le debut seul, obtenu %+v", before)
	}
	after := collectLines(t, bashOKLine)
	if len(after) != 0 {
		t.Fatalf("un resultat sans debut dans la tentative ne doit rien emettre, obtenu %+v", after)
	}
}

func TestToolCallWithoutIDStaysAPlainToolEvent(t *testing.T) {
	got := collectLines(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}}`)
	if len(got) != 1 || got[0].Kind != contracts.BackendKindTool || got[0].Tool != "Read" || got[0].ToolCall != nil {
		t.Fatalf("outil sans id: %+v", got)
	}
}

func TestEveryEmittedToolCallIsValid(t *testing.T) {
	got := collectLines(t,
		bashStartLine,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_02","name":"Read","input":{"file_path":"/etc/hosts"}},{"type":"tool_use","id":"agent_01","name":"Task","input":{"description":"chercher","subagent_type":"Explore"}}]},"session_id":"s"}`,
		bashOKLine,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_02","content":[{"type":"text","text":"permission denied"}],"is_error":true},{"type":"tool_result","tool_use_id":"agent_01"}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"x"}}]},"session_id":"s"}`,
	)
	var starts, ends int
	for i, ev := range got {
		if err := ev.ValidateToolCall(); err != nil {
			t.Fatalf("evenement %d refuse: %v (%+v)", i, err, ev)
		}
		if ev.Kind == contracts.BackendKindTool && ev.ToolCall != nil {
			starts++
		}
		if ev.Kind == contracts.BackendKindToolDone {
			ends++
		}
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("debuts = %d, fins = %d, want 2 et 2: %+v", starts, ends, got)
	}
}
