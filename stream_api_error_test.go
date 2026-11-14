package claude

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const syntheticAPIErrorText = "API Error: rate limit reached"

func TestReadTurnDropsAPIErrorMessageText(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		flagged string
	}{
		{"flag and synthetic model", `"model":"<synthetic>"`, "true"},
		{"flag on a named model", `"model":"claude-haiku-5-5"`, "true"},
		{"synthetic model without flag", `"model":"<synthetic>"`, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canned := fmt.Sprintf(`{"type":"assistant","message":{%s,"content":[{"type":"text","text":%q}]},"isApiErrorMessage":%s,"error":"rate_limit","session_id":"s"}`, tc.model, syntheticAPIErrorText, tc.flagged) + "\n" +
				fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":%q,"session_id":"s"}`, syntheticAPIErrorText) + "\n"

			var got []contracts.BackendEvent
			turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), func(e contracts.BackendEvent) { got = append(got, e) })
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range got {
				if e.Kind == "text" {
					t.Fatalf("API error message leaked as model text: %+v", got)
				}
			}
			if !turn.IsError || turn.ErrMsg != syntheticAPIErrorText {
				t.Fatalf("error must still surface through the result: %+v", turn)
			}
		})
	}
}

func TestReadTurnKeepsModelText(t *testing.T) {
	canned := `{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[{"type":"text","text":"PONG"}]},"session_id":"s"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"PONG","session_id":"s"}` + "\n"

	var texts []string
	turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), func(e contracts.BackendEvent) {
		if e.Kind == "text" {
			texts = append(texts, e.Detail)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.IsError {
		t.Fatal("did not expect error")
	}
	if !slices.Equal(texts, []string{"PONG"}) {
		t.Fatalf("text events = %q, want [PONG]", texts)
	}
}
