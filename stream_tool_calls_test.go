package claude

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	bashStartLine = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"go test ./..."}}]},"session_id":"s"}`
	bashOKLine    = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok  \tclaude\t0.4s"}]},"session_id":"s"}`
	turnEndLine   = `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}`
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
	before := runTurn(t, bashStartLine)
	if len(before) == 0 || before[0].ToolCall == nil || before[0].ToolCall.ID != "toolu_01" {
		t.Fatalf("attendu le debut dans la premiere tentative, obtenu %+v", before)
	}
	after := runTurn(t, bashOKLine)
	for _, ev := range after {
		if ev.Kind == contracts.BackendKindToolDone {
			t.Fatalf("un resultat sans debut dans la tentative ne doit rien emettre, obtenu %+v", after)
		}
	}
}

func TestCallBeyondTheOpenBoundEmitsNoToolCall(t *testing.T) {
	var lines []string
	for i := range maxOpenToolCalls + 1 {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	for i := range maxOpenToolCalls + 1 {
		lines = append(lines, bashResultWithID(callID(i)))
	}
	got := collectLines(t, lines...)
	const overflow = maxOpenToolCalls
	if len(got) != 2*maxOpenToolCalls+1 {
		t.Fatalf("attendu %d evenements (256 debuts, un simple evenement tool, 256 fins), obtenu %d", 2*maxOpenToolCalls+1, len(got))
	}
	want := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: "Bash", Detail: "x"}
	if !sameEvent(t, got[overflow], want) {
		t.Fatalf("257e debut = %+v, want un simple evenement tool %+v", got[overflow], want)
	}
	for i := range overflow {
		if got[i].ToolCall == nil || got[i].ToolCall.ID != callID(i) || got[i].ToolCall.State != contracts.ToolCallStarted {
			t.Fatalf("debut %d = %+v, attendu le debut de %s", i, got[i], callID(i))
		}
	}
	for i, ev := range got[overflow+1:] {
		if ev.Kind != contracts.BackendKindToolDone || ev.ToolCall == nil || ev.ToolCall.ID != callID(i) {
			t.Fatalf("fin %d = %+v, attendu la fin de %s seule", i, ev, callID(i))
		}
	}
}

func TestRepeatedIDBeyondTheOpenBoundNeverStarts(t *testing.T) {
	var lines []string
	for i := range maxOpenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	overflowID := callID(maxOpenToolCalls)
	lines = append(lines, bashStartWithID(overflowID), bashStartWithID(overflowID))
	got := collectLines(t, lines...)
	if len(got) != maxOpenToolCalls+2 {
		t.Fatalf("attendu %d evenements, obtenu %d", maxOpenToolCalls+2, len(got))
	}
	for i, ev := range got[maxOpenToolCalls:] {
		if ev.Kind != contracts.BackendKindTool || ev.ToolCall != nil {
			t.Fatalf("debut %d au-dela de la borne = %+v, attendu un simple evenement tool", i, ev)
		}
	}
	var starts int
	for _, ev := range got {
		if ev.ToolCall != nil && ev.ToolCall.State == contracts.ToolCallStarted {
			starts++
		}
	}
	if starts != maxOpenToolCalls {
		t.Fatalf("debuts = %d, want %d: un ID au-dela de la borne ne doit jamais demarrer", starts, maxOpenToolCalls)
	}
}

func TestDuplicateStartOfAnOpenCallIsAPlainToolEvent(t *testing.T) {
	duplicate := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/x"}}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, duplicate, bashOKLine)
	if len(got) != 3 {
		t.Fatalf("attendu trois evenements, obtenu %+v", got)
	}
	if got[1].Kind != contracts.BackendKindTool || got[1].Tool != "Read" || got[1].ToolCall != nil {
		t.Fatalf("doublon attendu en simple evenement tool, obtenu %+v", got[1])
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[2], want) {
		t.Fatalf("fin = %+v, want %+v", got[2], want)
	}
}

func runTurn(t *testing.T, lines ...string) []contracts.BackendEvent {
	t.Helper()
	var got []contracts.BackendEvent
	source := strings.Join(append(lines, turnEndLine), "\n") + "\n"
	_, err := readTurn(context.Background(), startLineReader(strings.NewReader(source), nil), nil, func(e contracts.BackendEvent) { got = append(got, e) }, nil)
	if err != nil {
		t.Fatalf("readTurn: %v", err)
	}
	return got
}

func callID(i int) string {
	return fmt.Sprintf("toolu_%03d", i)
}

func bashStartWithID(id string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"x"}}]},"session_id":"s"}`, id)
}

func bashResultWithID(id string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]},"session_id":"s"}`, id)
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
