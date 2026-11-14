package claude

import (
	"errors"
	"fmt"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func errorResultLine(result, errorsJSON string) string {
	return fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":%q,"session_id":"s","errors":%s}`, result, errorsJSON) + "\n"
}

func TestRespondDetailsTheResultErrorsWhenResultIsEmpty(t *testing.T) {
	_, err := respondWithStream(t, errorResultLine("", `["API quota exhausted","","  rate limit  "]`), nil)
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
	}
	if want := "API quota exhausted\nrate limit"; err.Error() != want {
		t.Fatalf("error = %q, want the non blank errors joined as %q", err.Error(), want)
	}
}

func TestRespondIgnoresErrorsOfAnotherShape(t *testing.T) {
	fallback := localizedTurnFailure(contracts.Prompt{}.Language)
	cases := []struct {
		name       string
		errorsJSON string
	}{
		{"empty array", `[]`},
		{"null", `null`},
		{"element is not a string", `["boom",42]`},
		{"element is an array", `[["boom"]]`},
		{"errors is a string", `"boom"`},
		{"errors is an object", `{"message":"boom"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := respondWithStream(t, errorResultLine("", tc.errorsJSON), nil)
			if !errors.Is(err, ErrTurnFailed) {
				t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
			}
			if err.Error() != fallback {
				t.Fatalf("error = %q, want the fallback label %q", err.Error(), fallback)
			}
		})
	}
}

func TestRespondKeepsResultTextOverErrors(t *testing.T) {
	const reported = "Quota exhausted for this account"
	_, err := respondWithStream(t, errorResultLine(reported, `["boom"]`), nil)
	if err == nil || err.Error() != reported {
		t.Fatalf("error = %v, want the result text %q", err, reported)
	}
}

func TestRespondPrefersErrorsOverAPIErrorText(t *testing.T) {
	stream := apiErrorMessageLine(syntheticAPIErrorText) + "\n" +
		errorResultLine("", `["Authentication failed"]`)
	_, err := respondWithStream(t, stream, nil)
	if err == nil || err.Error() != "Authentication failed" {
		t.Fatalf("error = %v, want the errors detail %q", err, "Authentication failed")
	}
}

func TestRespondFallsBackToAPIErrorTextWhenErrorsAreEmpty(t *testing.T) {
	stream := apiErrorMessageLine(syntheticAPIErrorText) + "\n" +
		errorResultLine("", `[]`)
	_, err := respondWithStream(t, stream, nil)
	if err == nil || err.Error() != syntheticAPIErrorText {
		t.Fatalf("error = %v, want the API error detail %q", err, syntheticAPIErrorText)
	}
}
