package claude

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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
		t.Fatalf("want one event, got %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindTool,
		Tool:     "Bash",
		Detail:   "go test ./...",
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", Detail: "go test ./...", State: contracts.ToolCallStarted},
	}
	if !sameEvent(t, got[0], want) {
		t.Fatalf("start = %+v, want %+v", got[0], want)
	}
}

func TestSuccessfulToolResultClosesTheCall(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine)
	if len(got) != 2 {
		t.Fatalf("want two events, got %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[1], want) {
		t.Fatalf("done = %+v, want %+v", got[1], want)
	}
}

func TestFailedToolResultCarriesItsReason(t *testing.T) {
	failure := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"exit status 1: missing go.sum entry","is_error":true}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, failure)
	if len(got) != 2 {
		t.Fatalf("want two events, got %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallFailed, Error: "exit status 1: missing go.sum entry"},
	}
	if !sameEvent(t, got[1], want) {
		t.Fatalf("failure = %+v, want %+v", got[1], want)
	}
}

func TestFailedToolResultReasonIsBoundedOnARuneBoundary(t *testing.T) {
	long := "a" + strings.Repeat("é", 300)
	failure := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"` + long + `","is_error":true}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, failure)
	if len(got) != 2 || got[1].ToolCall == nil {
		t.Fatalf("want a failure, got %+v", got)
	}
	reason := got[1].ToolCall.Error
	if !utf8.ValidString(reason) {
		t.Fatalf("reason cut in the middle of a character: %q", reason)
	}
	if got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall.ID != "toolu_01" || got[1].ToolCall.State != contracts.ToolCallFailed {
		t.Fatalf("done = %+v, want the failure of toolu_01", got[1])
	}
	if n := utf8.RuneCountInString(reason); n == 0 || n > maxToolErrorRunes {
		t.Fatalf("reason of %d characters, want between 1 and %d", n, maxToolErrorRunes)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(reason, "…")) {
		t.Fatalf("reason %q is not the start of the result", reason)
	}
}

func TestFailedToolResultReadsItsTextPastAStrayElement(t *testing.T) {
	failure := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":["stray",{"type":"text","text":"permission denied"}],"is_error":true}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, failure)
	if len(got) != 2 || got[1].ToolCall == nil {
		t.Fatalf("want a failure, got %+v", got)
	}
	if got[1].ToolCall.State != contracts.ToolCallFailed || got[1].ToolCall.Error != "permission denied" {
		t.Fatalf("failure = %+v, want the text block as its reason", got[1].ToolCall)
	}
}

func TestNonObjectContentElementKeepsTheToolDone(t *testing.T) {
	line := `{"type":"user","message":{"content":["free text",{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, line)
	if len(got) != 2 || got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall == nil {
		t.Fatalf("a non-object element must not cost the tool_result after it, got %+v", got)
	}
	if got[1].ToolCall.State != contracts.ToolCallSucceeded {
		t.Fatalf("done = %+v, want succeeded", got[1].ToolCall)
	}
}

func TestNonObjectAssistantElementKeepsTheTextAndToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":["stray",{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"go test ./..."}}]},"session_id":"s"}`
	got := collectLines(t, line)
	if len(got) != 2 {
		t.Fatalf("want the text and the tool start, got %+v", got)
	}
	if got[0].Kind != "text" || got[0].Detail != "hello" {
		t.Fatalf("text = %+v, want hello", got[0])
	}
	if got[1].ToolCall == nil || got[1].ToolCall.ID != "toolu_01" || got[1].ToolCall.State != contracts.ToolCallStarted {
		t.Fatalf("tool start = %+v, want the start of toolu_01", got[1])
	}
}

func TestSubagentWithStringErrorFlagClosesLikeMaster(t *testing.T) {
	start := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"agent_01","name":"Task","input":{"description":"look","subagent_type":"Explore"}}]},"session_id":"s"}`
	end := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"agent_01","content":"done","is_error":"false"}]},"session_id":"s"}`
	got := runTurn(t, start, end)
	if len(got) != 3 {
		t.Fatalf("want subagent start, subagent done and result, got %+v", got)
	}
	wantStart := contracts.BackendEvent{Kind: "subagent", Subagent: &contracts.Subagent{ID: "agent_01", Name: "look", Kind: "Explore", State: "active"}}
	if !sameEvent(t, got[0], wantStart) {
		t.Fatalf("subagent start = %+v, want %+v", got[0], wantStart)
	}
	wantDone := contracts.BackendEvent{Kind: "subagent", Subagent: &contracts.Subagent{ID: "agent_01", State: "done"}}
	if !sameEvent(t, got[1], wantDone) {
		t.Fatalf("subagent done = %+v, want %+v", got[1], wantDone)
	}
	if got[2].Kind != "result" {
		t.Fatalf("last event = %+v, want result", got[2])
	}
}

func TestSecondResultOfACallEmitsNothing(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine, bashOKLine)
	if len(got) != 2 {
		t.Fatalf("a call closes only once, got %+v", got)
	}
}

func TestResultOfACallStartedBeforeAResetEmitsNothing(t *testing.T) {
	before := runTurn(t, bashStartLine)
	if len(before) == 0 || before[0].ToolCall == nil || before[0].ToolCall.ID != "toolu_01" {
		t.Fatalf("want the start in the first attempt, got %+v", before)
	}
	after := runTurn(t, bashOKLine)
	for _, ev := range after {
		if ev.Kind == contracts.BackendKindToolDone {
			t.Fatalf("a result without a start in its attempt must emit nothing, got %+v", after)
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
		t.Fatalf("want %d events (256 starts, one plain tool event, 256 dones), got %d", 2*maxOpenToolCalls+1, len(got))
	}
	want := contracts.BackendEvent{Kind: contracts.BackendKindTool, Tool: "Bash", Detail: "x"}
	if !sameEvent(t, got[overflow], want) {
		t.Fatalf("257th start = %+v, want a plain tool event %+v", got[overflow], want)
	}
	for i := range overflow {
		if got[i].ToolCall == nil || got[i].ToolCall.ID != callID(i) || got[i].ToolCall.State != contracts.ToolCallStarted {
			t.Fatalf("start %d = %+v, want the start of %s", i, got[i], callID(i))
		}
	}
	for i, ev := range got[overflow+1:] {
		if ev.Kind != contracts.BackendKindToolDone || ev.ToolCall == nil || ev.ToolCall.ID != callID(i) {
			t.Fatalf("done %d = %+v, want the done of %s alone", i, ev, callID(i))
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
		t.Fatalf("want %d events, got %d", maxOpenToolCalls+2, len(got))
	}
	for i, ev := range got[maxOpenToolCalls:] {
		if ev.Kind != contracts.BackendKindTool || ev.ToolCall != nil {
			t.Fatalf("start %d beyond the bound = %+v, want a plain tool event", i, ev)
		}
	}
	var starts int
	for _, ev := range got {
		if ev.ToolCall != nil && ev.ToolCall.State == contracts.ToolCallStarted {
			starts++
		}
	}
	if starts != maxOpenToolCalls {
		t.Fatalf("starts = %d, want %d: an ID beyond the bound must never start", starts, maxOpenToolCalls)
	}
}

func TestStartRefusedByTheOpenBoundStaysBareAfterASlotFrees(t *testing.T) {
	var lines []string
	for i := range maxOpenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	refused := callID(maxOpenToolCalls)
	lines = append(lines, bashStartWithID(refused), bashResultWithID(callID(0)), bashStartWithID(refused), bashResultWithID(refused))
	got := collectLines(t, lines...)
	if len(got) != maxOpenToolCalls+3 {
		t.Fatalf("want %d events, got %d", maxOpenToolCalls+3, len(got))
	}
	for _, index := range []int{maxOpenToolCalls, maxOpenToolCalls + 2} {
		if got[index].Kind != contracts.BackendKindTool || got[index].Detail != "x" || got[index].ToolCall != nil {
			t.Fatalf("start of %s at event %d = %+v, want a plain tool event with its detail", refused, index, got[index])
		}
	}
	if got[maxOpenToolCalls+1].Kind != contracts.BackendKindToolDone || got[maxOpenToolCalls+1].ToolCall == nil || got[maxOpenToolCalls+1].ToolCall.ID != callID(0) {
		t.Fatalf("event %d = %+v, want the done of %s", maxOpenToolCalls+1, got[maxOpenToolCalls+1], callID(0))
	}
	var dones int
	for _, ev := range got {
		if ev.Kind == contracts.BackendKindToolDone {
			dones++
		}
	}
	if dones != 1 {
		t.Fatalf("dones = %d, want the done of %s alone", dones, callID(0))
	}
}

func TestCallIDBeyondTheLengthBoundNeverOpensACall(t *testing.T) {
	longID := strings.Repeat("x", maxCallIDBytes+1)
	got := collectLines(t, bashStartWithID(longID), bashResultWithID(longID))
	if len(got) != 1 || got[0].Kind != contracts.BackendKindTool || got[0].Detail != "x" || got[0].ToolCall != nil {
		t.Fatalf("events = %+v, want one plain tool event for an id over %d bytes", got, maxCallIDBytes)
	}
}

func TestCallIDAtTheLengthBoundOpensACall(t *testing.T) {
	boundID := strings.Repeat("x", maxCallIDBytes)
	got := collectLines(t, bashStartWithID(boundID), bashResultWithID(boundID))
	if len(got) != 2 || got[0].ToolCall == nil || got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall == nil || got[1].ToolCall.ID != boundID {
		t.Fatalf("events = %+v, want a started call and its done for an id of %d bytes", got, maxCallIDBytes)
	}
}

func TestTaskBeyondTheSeenBoundStartsNoSubagent(t *testing.T) {
	logs := captureDefaultLog(t)
	var lines []string
	for i := range maxSeenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)), bashResultWithID(callID(i)))
	}
	lines = append(lines, taskStartWithID("agent_over"), taskEndWithID("agent_over"))
	got := collectLines(t, lines...)
	if len(got) != 2*maxSeenToolCalls {
		t.Fatalf("want the %d events of the Bash calls and nothing for the Task, got %d", 2*maxSeenToolCalls, len(got))
	}
	for _, ev := range got {
		if ev.Kind == "subagent" {
			t.Fatalf("a Task beyond the seen bound started a subagent: %+v", ev)
		}
	}
	requireUnclaimedTaskWarning(t, logs)
	if n := strings.Count(logs.String(), "bound=max_seen"); n != 1 {
		t.Fatalf("max_seen reported %d times, want once: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), "agent_over") {
		t.Fatalf("the warning quotes the ID: %s", logs.String())
	}
}

func requireUnclaimedTaskWarning(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	if n := strings.Count(logs.String(), unclaimedTaskMessage); n != 1 {
		t.Fatalf("unclaimed Task warning reported %d times, want once: %s", n, logs.String())
	}
}

func taskStartWithID(id string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":%q,"name":"Task","input":{"description":"look","subagent_type":"Explore"}}]},"session_id":"s"}`, id)
}

func taskEndWithID(id string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"done"}]},"session_id":"s"}`, id)
}

func TestTaskReusingAnOpenCallIDStartsNoSubagent(t *testing.T) {
	got := collectLines(t, bashStartLine, taskStartWithID("toolu_01"))
	if len(got) != 1 || got[0].Kind != contracts.BackendKindTool || got[0].ToolCall == nil {
		t.Fatalf("want only the start of the Bash call, got %+v", got)
	}
}

func TestResultOfATaskReusingAnOpenCallIDClosesThatCall(t *testing.T) {
	got := collectLines(t, bashStartLine, taskStartWithID("toolu_01"), bashOKLine)
	if len(got) != 2 || got[0].ToolCall == nil || got[0].ToolCall.State != contracts.ToolCallStarted {
		t.Fatalf("want the start of the Bash call first, got %+v", got)
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[1], want) {
		t.Fatalf("done = %+v, want the first result to close the Bash call: %+v", got[1], want)
	}
}

func TestSecondResultAfterADuplicateTaskEmitsNothing(t *testing.T) {
	got := collectLines(t, bashStartLine, taskStartWithID("toolu_01"), bashOKLine, bashOKLine)
	if len(got) != 2 || got[1].Kind != contracts.BackendKindToolDone {
		t.Fatalf("want the start and one done, the second result must emit nothing, got %+v", got)
	}
}

func TestTodoWriteOnAnOpenCallIDLetsItsResultClose(t *testing.T) {
	todo := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"TodoWrite","input":{"todos":[{"content":"lire","status":"pending"}]}}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, todo, bashOKLine)
	if len(got) != 3 || got[1].Kind != "todos" || got[2].Kind != contracts.BackendKindToolDone || got[2].ToolCall == nil || got[2].ToolCall.ID != "toolu_01" {
		t.Fatalf("want the start, the todos and the done of toolu_01, got %+v", got)
	}
}

func TestTaskWithEmptyIDStartsNoSubagent(t *testing.T) {
	logs := captureDefaultLog(t)
	got := collectLines(t, taskStartWithID(""), taskEndWithID(""))
	if len(got) != 0 {
		t.Fatalf("want no event for a Task with an empty ID, got %+v", got)
	}
	requireUnclaimedTaskWarning(t, logs)
	if strings.Contains(logs.String(), "id=") {
		t.Fatalf("the warning carries an ID attribute: %s", logs.String())
	}
}

func TestTaskWithTooLongIDStartsNoSubagent(t *testing.T) {
	logs := captureDefaultLog(t)
	const distinctive = "toolu_oversized_"
	id := distinctive + strings.Repeat("a", maxCallIDBytes)
	got := collectLines(t, taskStartWithID(id), taskEndWithID(id))
	if len(got) != 0 {
		t.Fatalf("want no event for a Task whose ID is %d bytes, got %+v", len(id), got)
	}
	requireUnclaimedTaskWarning(t, logs)
	if strings.Contains(logs.String(), distinctive) {
		t.Fatalf("the warning quotes the ID: %s", logs.String())
	}
}

func TestTodoWriteDoesNotClaimItsID(t *testing.T) {
	todo := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"TodoWrite","input":{"todos":[{"content":"lire","status":"pending"}]}}]},"session_id":"s"}`
	got := collectLines(t, todo, bashStartLine, bashOKLine)
	if len(got) != 3 {
		t.Fatalf("want the todos, the start of the Bash call and its done, got %+v", got)
	}
	if got[0].Kind != "todos" {
		t.Fatalf("first event: got %+v, want the todos", got[0])
	}
	startWant := contracts.BackendEvent{
		Kind:     contracts.BackendKindTool,
		Tool:     "Bash",
		Detail:   "go test ./...",
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", Detail: "go test ./...", State: contracts.ToolCallStarted},
	}
	if !sameEvent(t, got[1], startWant) {
		t.Fatalf("start = %+v, want the Bash call to start under toolu_01: %+v", got[1], startWant)
	}
	doneWant := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[2], doneWant) {
		t.Fatalf("done = %+v, want the Bash call to close under toolu_01: %+v", got[2], doneWant)
	}
}

func TestTaskReusingAFinishedCallIDStartsNoSubagent(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine, taskStartWithID("toolu_01"))
	if len(got) != 2 || got[0].Kind != contracts.BackendKindTool || got[1].Kind != contracts.BackendKindToolDone {
		t.Fatalf("want the start and the end of the Bash call and nothing for the Task, got %+v", got)
	}
}

func TestFirstTaskKeepsItsSubagentWhenAnotherTaskReusesItsID(t *testing.T) {
	got := collectLines(t, taskStartWithID("agent_01"), taskStartWithID("agent_01"), taskEndWithID("agent_01"))
	if len(got) != 2 {
		t.Fatalf("want the start and the end of the first Task, got %+v", got)
	}
	if got[0].Kind != "subagent" || got[0].Subagent == nil || got[0].Subagent.State != "active" {
		t.Fatalf("subagent start: got %+v, want active agent_01", got[0])
	}
	if got[1].Kind != "subagent" || got[1].Subagent == nil || got[1].Subagent.State != "done" {
		t.Fatalf("subagent end: got %+v, want done agent_01", got[1])
	}
}

func TestDuplicateTaskWarnsOncePerTurnWithoutItsID(t *testing.T) {
	logs := captureDefaultLog(t)
	collectLines(t, bashStartLine, taskStartWithID("toolu_01"), taskStartWithID("toolu_01"))
	if n := strings.Count(logs.String(), unclaimedTaskMessage); n != 1 {
		t.Fatalf("duplicate Task warning reported %d times, want once: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), "toolu_01") {
		t.Fatalf("the warning quotes the ID: %s", logs.String())
	}
}

func TestOpenBoundIsReportedOncePerTurn(t *testing.T) {
	logs := captureDefaultLog(t)
	var lines []string
	for i := range maxOpenToolCalls + 2 {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	collectLines(t, lines...)
	if n := strings.Count(logs.String(), "bound=max_open"); n != 1 {
		t.Fatalf("max_open reported %d times, want once: %s", n, logs.String())
	}
}

func TestSeenBoundIsReportedOncePerTurn(t *testing.T) {
	logs := captureDefaultLog(t)
	var lines []string
	for i := range maxSeenToolCalls + 2 {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	collectLines(t, lines...)
	if n := strings.Count(logs.String(), "bound=max_seen"); n != 1 {
		t.Fatalf("max_seen reported %d times, want once: %s", n, logs.String())
	}
}

func TestEachReadTurnReportsItsOwnBoundWarnings(t *testing.T) {
	logs := captureDefaultLog(t)
	starts := boundStartLines(maxSeenToolCalls + 1)
	runTurn(t, starts...)
	runTurn(t, starts...)
	for _, bound := range []string{"max_open", "max_seen"} {
		if n := strings.Count(logs.String(), "bound="+bound); n != 2 {
			t.Fatalf("%s reported %d times over two turns, want once per turn: %s", bound, n, logs.String())
		}
	}
}

func boundStartLines(n int) []string {
	lines := make([]string, 0, n)
	for i := range n {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	return lines
}

func toolStartWithName(id, name string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":%q,"name":%q,"input":{"command":"x"}}]},"session_id":"s"}`, id, name)
}

func TestToolNameBeyondTheLengthBoundNeverOpensACall(t *testing.T) {
	longName := strings.Repeat("x", maxToolNameBytes+1)
	got := collectLines(t, toolStartWithName("toolu_long", longName), bashResultWithID("toolu_long"))
	if len(got) != 1 || got[0].Kind != contracts.BackendKindTool || got[0].ToolCall != nil {
		t.Fatalf("events = %+v, want one plain tool event for a name over %d bytes", got, maxToolNameBytes)
	}
}

func TestToolNameAtTheLengthBoundOpensACall(t *testing.T) {
	boundName := strings.Repeat("x", maxToolNameBytes)
	got := collectLines(t, toolStartWithName("toolu_edge", boundName), bashResultWithID("toolu_edge"))
	if len(got) != 2 || got[0].ToolCall == nil || got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall == nil || got[1].ToolCall.ID != "toolu_edge" {
		t.Fatalf("events = %+v, want a started call and its done for a name of %d bytes", got, maxToolNameBytes)
	}
}

func TestIDOfARefusedToolNameStaysSeen(t *testing.T) {
	got := collectLines(t, toolStartWithName("toolu_refused", ""), toolStartWithName("toolu_refused", "Bash"))
	if len(got) != 2 || got[0].ToolCall != nil || got[1].ToolCall != nil {
		t.Fatalf("events = %+v, want two plain tool events: the refused name claims its ID, so the valid restart never opens a call", got)
	}
}

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestFinishedCallIDNeverRestarts(t *testing.T) {
	got := collectLines(t, bashStartLine, bashOKLine, bashStartLine, bashOKLine)
	if len(got) != 3 {
		t.Fatalf("want start, done and one plain tool event, got %+v", got)
	}
	if got[2].Kind != contracts.BackendKindTool || got[2].Tool != "Bash" || got[2].ToolCall != nil {
		t.Fatalf("restart of a finished ID = %+v, want a plain tool event", got[2])
	}
}

func TestCallBeyondTheSeenBoundEmitsNoToolCall(t *testing.T) {
	var lines []string
	for i := range maxSeenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)), bashResultWithID(callID(i)))
	}
	overflow := callID(maxSeenToolCalls)
	lines = append(lines, bashStartWithID(overflow), bashResultWithID(overflow))
	got := collectLines(t, lines...)
	if len(got) != 2*maxSeenToolCalls+1 {
		t.Fatalf("want %d events, got %d", 2*maxSeenToolCalls+1, len(got))
	}
	last := got[2*maxSeenToolCalls]
	if last.Kind != contracts.BackendKindTool || last.ToolCall != nil {
		t.Fatalf("start beyond the seen bound = %+v, want a plain tool event", last)
	}
}

func TestClosingACallFreesAnOpenSlot(t *testing.T) {
	var lines []string
	for i := range maxOpenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	next := callID(maxOpenToolCalls)
	lines = append(lines, bashResultWithID(callID(0)), bashStartWithID(next))
	got := collectLines(t, lines...)
	if len(got) != maxOpenToolCalls+2 {
		t.Fatalf("want %d events, got %d", maxOpenToolCalls+2, len(got))
	}
	last := got[len(got)-1]
	if last.ToolCall == nil || last.ToolCall.ID != next || last.ToolCall.State != contracts.ToolCallStarted {
		t.Fatalf("start after a close = %+v, want the start of %s", last, next)
	}
}

func TestDuplicateStartOfAnOpenCallIsAPlainToolEvent(t *testing.T) {
	duplicate := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/x"}}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, duplicate, bashOKLine)
	if len(got) != 3 {
		t.Fatalf("want three events, got %+v", got)
	}
	if got[1].Kind != contracts.BackendKindTool || got[1].Tool != "Read" || got[1].ToolCall != nil {
		t.Fatalf("duplicate = %+v, want a plain tool event", got[1])
	}
	want := contracts.BackendEvent{
		Kind:     contracts.BackendKindToolDone,
		ToolCall: &contracts.ToolCall{ID: "toolu_01", Tool: "Bash", State: contracts.ToolCallSucceeded},
	}
	if !sameEvent(t, got[2], want) {
		t.Fatalf("done = %+v, want %+v", got[2], want)
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
		t.Fatalf("tool without id: %+v", got)
	}
}

func TestEveryEmittedToolCallIsValid(t *testing.T) {
	got := collectLines(t,
		bashStartLine,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_02","name":"Read","input":{"file_path":"/etc/hosts"}},{"type":"tool_use","id":"agent_01","name":"Task","input":{"description":"search","subagent_type":"Explore"}}]},"session_id":"s"}`,
		bashOKLine,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_02","content":[{"type":"text","text":"permission denied"}],"is_error":true},{"type":"tool_result","tool_use_id":"agent_01"}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"x"}}]},"session_id":"s"}`,
	)
	var starts, ends int
	for i, ev := range got {
		if err := ev.ValidateToolCall(); err != nil {
			t.Fatalf("event %d refused: %v (%+v)", i, err, ev)
		}
		if ev.Kind == contracts.BackendKindTool && ev.ToolCall != nil {
			starts++
		}
		if ev.Kind == contracts.BackendKindToolDone {
			ends++
		}
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("starts = %d, dones = %d, want 2 and 2: %+v", starts, ends, got)
	}
}
