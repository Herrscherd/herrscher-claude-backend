package claude

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const syntheticAPIErrorText = "API Error: rate limit reached"

func apiErrorMessageLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]},"isApiErrorMessage":true,"session_id":"s"}`, text)
}

func apiErrorStream(text, result string) string {
	return apiErrorMessageLine(text) + "\n" +
		fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":%q,"session_id":"s"}`, result) + "\n"
}

func respondWithStream(t *testing.T, stream string, onEvent func(contracts.BackendEvent)) (string, error) {
	t.Helper()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.spawn = func(string) (*streamSession, error) {
		return newStreamSession(discardWriteCloser{}, strings.NewReader(stream)), nil
	}
	return r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, onEvent)
}

func TestReadTurnDropsAPIErrorMessageContent(t *testing.T) {
	cases := []struct {
		name  string
		model string
	}{
		{"flag on the synthetic model", `"model":"<synthetic>"`},
		{"flag on a named model", `"model":"claude-haiku-5-5"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagged := fmt.Sprintf(`{"type":"assistant","message":{%s,"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":%q},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":5,"output_tokens":1}},"isApiErrorMessage":true,"session_id":"s"}`, tc.model, syntheticAPIErrorText) + "\n" +
				fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":%q,"session_id":"s"}`, syntheticAPIErrorText) + "\n"

			var got []contracts.BackendEvent
			turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(flagged), nil), func(e contracts.BackendEvent) { got = append(got, e) })
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range got {
				if e.Kind != "result" {
					t.Fatalf("API error message emitted %q content: %+v", e.Kind, got)
				}
			}
			if !turn.IsError || turn.ErrMsg != syntheticAPIErrorText {
				t.Fatalf("error must still surface through the result: %+v", turn)
			}
		})
	}
}

func TestReadTurnKeepsContentOfSyntheticModelWithoutFlag(t *testing.T) {
	canned := `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Checking"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},"session_id":"s"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"Checking","session_id":"s"}` + "\n"

	var kinds []string
	turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), func(e contracts.BackendEvent) {
		if e.Kind != "result" {
			kinds = append(kinds, e.Kind+"|"+e.Tool+"|"+e.Detail)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.IsError {
		t.Fatal("did not expect error")
	}
	if want := []string{"text||Checking", "tool|Bash|ls"}; !slices.Equal(kinds, want) {
		t.Fatalf("events = %q, want %q", kinds, want)
	}
}

func TestRespondSurfacesAPIErrorTextWhenResultIsEmpty(t *testing.T) {
	_, err := respondWithStream(t, apiErrorStream(syntheticAPIErrorText, ""), nil)
	if err == nil {
		t.Fatal("expected the API error to fail the turn")
	}
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
	}
	if err.Error() != syntheticAPIErrorText {
		t.Fatalf("error = %q, want the API error detail %q", err.Error(), syntheticAPIErrorText)
	}
}

func TestRespondKeepsResultTextOverAPIErrorText(t *testing.T) {
	const reported = "Quota exhausted for this account"
	_, err := respondWithStream(t, apiErrorStream(syntheticAPIErrorText, reported), func(contracts.BackendEvent) {})
	if err == nil {
		t.Fatal("expected the API error to fail the turn")
	}
	if err.Error() != reported {
		t.Fatalf("error = %q, want the result text %q", err.Error(), reported)
	}
}

func TestRespondSucceedsWhenAPIErrorMarkerPrecedesSuccessfulResult(t *testing.T) {
	stream := apiErrorMessageLine(syntheticAPIErrorText) + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"PONG","session_id":"s"}` + "\n"
	got, err := respondWithStream(t, stream, nil)
	if err != nil {
		t.Fatalf("a successful result must not fail the turn: %v", err)
	}
	if got != "PONG" {
		t.Fatalf("reply = %q, want PONG", got)
	}
}

func TestRespondUsesLastNonEmptyAPIErrorText(t *testing.T) {
	const older, newer = "API Error: first attempt", "API Error: second attempt"
	stream := apiErrorMessageLine(older) + "\n" +
		apiErrorMessageLine(newer) + "\n" +
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"thinking","thinking":"hmm"}]},"isApiErrorMessage":true,"session_id":"s"}` + "\n" +
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","session_id":"s"}` + "\n"
	_, err := respondWithStream(t, stream, nil)
	if err == nil {
		t.Fatal("expected the API error to fail the turn")
	}
	if err.Error() != newer {
		t.Fatalf("error = %q, want the last API error detail %q", err.Error(), newer)
	}
}

func TestReadTurnReadsModelTextWhenAPIErrorMarkerIsUnexpected(t *testing.T) {
	cases := []struct {
		name   string
		marker string
		model  string
	}{
		{"marker is a string", `"isApiErrorMessage":"yes"`, `"model":"claude-haiku-5-5"`},
		{"marker is the string true", `"isApiErrorMessage":"true"`, `"model":"claude-haiku-5-5"`},
		{"marker is a number", `"isApiErrorMessage":1`, `"model":"claude-haiku-5-5"`},
		{"marker is null", `"isApiErrorMessage":null`, `"model":"claude-haiku-5-5"`},
		{"model is not a string", `"isApiErrorMessage":false`, `"model":42`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canned := fmt.Sprintf(`{"type":"assistant",%s,"message":{%s,"content":[{"type":"text","text":"PONG"}]},"session_id":"s"}`, tc.marker, tc.model) + "\n" +
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
