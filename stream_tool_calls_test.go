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
	if n := utf8.RuneCountInString(reason); n == 0 || n > maxToolErrorRunes {
		t.Fatalf("reason of %d characters, want between 1 and %d", n, maxToolErrorRunes)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(reason, "…")) {
		t.Fatalf("reason %q is not the start of the result", reason)
	}
}

func TestStringErrorFlagKeepsTheToolDone(t *testing.T) {
	stringFlag := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"exit status 1","is_error":"true"}]},"session_id":"s"}`
	got := collectLines(t, bashStartLine, stringFlag)
	if len(got) != 2 || got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall == nil {
		t.Fatalf("the tool_done must survive a string is_error, got %+v", got)
	}
	if got[1].ToolCall.State != contracts.ToolCallSucceeded || got[1].ToolCall.Error != "" {
		t.Fatalf("only the JSON literal true marks an error, got %+v", got[1].ToolCall)
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

func TestSubagentWithStringErrorFlagClosesLikeMaster(t *testing.T) {
	start := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"agent_01","name":"Task","input":{"description":"look","subagent_type":"Explore"}}]},"session_id":"s"}`
	end := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"agent_01","content":"done","is_error":"false"}]},"session_id":"s"}`
	got := runTurn(t, start, end)
	if len(got) != 3 {
		t.Fatalf("want subagent start, subagent done and result, got %+v", got)
	}
	start := contracts.BackendEvent{Kind: "subagent", Subagent: &contracts.Subagent{ID: "agent_01", Name: "look", Kind: "Explore", State: "active"}}
	if !sameEvent(t, got[0], start) {
		t.Fatalf("subagent start = %+v, want %+v", got[0], start)
	}
	done := contracts.BackendEvent{Kind: "subagent", Subagent: &contracts.Subagent{ID: "agent_01", State: "done"}}
	if !sameEvent(t, got[1], done) {
		t.Fatalf("subagent done = %+v, want %+v", got[1], done)
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
