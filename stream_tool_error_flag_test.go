package claude

import (
	"fmt"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func resultWithErrorFlag(id, flag string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"exit status 1","is_error":%s}]},"session_id":"s"}`, id, flag)
}

func TestNonBooleanErrorFlagEmitsNoDone(t *testing.T) {
	cases := []struct {
		name string
		flag string
	}{
		{name: "string true", flag: `"true"`},
		{name: "number 1", flag: `1`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := collectLines(t, bashStartLine, resultWithErrorFlag("toolu_01", c.flag))
			if len(got) != 1 || got[0].Kind != contracts.BackendKindTool || got[0].ToolCall == nil {
				t.Fatalf("want the start alone, got %+v", got)
			}
		})
	}
}

func TestErrorFlagDecidesTheStateOfTheClosedCall(t *testing.T) {
	cases := []struct {
		name string
		flag string
		want contracts.ToolCallState
	}{
		{name: "null is absent", flag: `null`, want: contracts.ToolCallSucceeded},
		{name: "false", flag: `false`, want: contracts.ToolCallSucceeded},
		{name: "true", flag: `true`, want: contracts.ToolCallFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := collectLines(t, bashStartLine, resultWithErrorFlag("toolu_01", c.flag))
			if len(got) != 2 || got[1].Kind != contracts.BackendKindToolDone || got[1].ToolCall == nil {
				t.Fatalf("want the tool_done, got %+v", got)
			}
			if got[1].ToolCall.State != c.want {
				t.Fatalf("state = %v, want %v", got[1].ToolCall.State, c.want)
			}
		})
	}
}

func TestNonBooleanErrorFlagFreesItsOpenSlot(t *testing.T) {
	var lines []string
	for i := range maxOpenToolCalls {
		lines = append(lines, bashStartWithID(callID(i)))
	}
	lines = append(lines, resultWithErrorFlag(callID(0), `"true"`), bashStartWithID(callID(maxOpenToolCalls)))
	got := collectLines(t, lines...)
	if len(got) != maxOpenToolCalls+1 {
		t.Fatalf("want %d events, got %d", maxOpenToolCalls+1, len(got))
	}
	if last := got[len(got)-1]; last.ToolCall == nil || last.ToolCall.ID != callID(maxOpenToolCalls) {
		t.Fatalf("the start after the freed slot = %+v, want a call", last)
	}
}

func TestNonBooleanErrorFlagKeepsTheIDSeen(t *testing.T) {
	got := collectLines(t, bashStartLine, resultWithErrorFlag("toolu_01", `1`), bashStartLine, bashOKLine)
	if len(got) != 2 || got[1].ToolCall != nil {
		t.Fatalf("want the start, then a plain tool event for the repeated ID, got %+v", got)
	}
}

func TestNonBooleanErrorFlagIsWarnedOncePerTurnWithoutItsValue(t *testing.T) {
	logs := captureDefaultLog(t)
	collectLines(t,
		bashStartWithID(callID(0)), resultWithErrorFlag(callID(0), `"oui-marqueur"`),
		bashStartWithID(callID(1)), resultWithErrorFlag(callID(1), `2`),
	)
	if n := strings.Count(logs.String(), "is_error is not a boolean"); n != 1 {
		t.Fatalf("warning reported %d times, want once: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), "marqueur") {
		t.Fatalf("the warning quotes the value: %s", logs.String())
	}
}
