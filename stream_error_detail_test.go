package claude

import (
	"errors"
	"fmt"
	"strings"
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

func TestRespondTrimsEveryErrorSource(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "padded result text is trimmed",
			stream: errorResultLine("  Quota exhausted  ", `[]`),
			want:   "Quota exhausted",
		},
		{
			name:   "blank result falls to the errors",
			stream: errorResultLine("   ", `["API quota exhausted"]`),
			want:   "API quota exhausted",
		},
		{
			name:   "blank result and blank errors fall to the marked text",
			stream: apiErrorMessageLine(syntheticAPIErrorText) + "\n" + errorResultLine(" \t ", `[" "]`),
			want:   syntheticAPIErrorText,
		},
		{
			name:   "blank result without errors falls to the marked text",
			stream: apiErrorStream(syntheticAPIErrorText, "   "),
			want:   syntheticAPIErrorText,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := respondWithStream(t, tc.stream, nil)
			if !errors.Is(err, ErrTurnFailed) {
				t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRespondBoundsTheErrorDetail(t *testing.T) {
	const bound = 2000
	long := strings.Repeat("é", bound+500)
	want := strings.Repeat("é", bound-1) + "…"
	cases := []struct {
		name   string
		stream string
	}{
		{name: "result text", stream: errorResultLine(long, `[]`)},
		{name: "errors", stream: errorResultLine("", fmt.Sprintf("[%q]", long))},
		{name: "marked text", stream: apiErrorStream(long, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := respondWithStream(t, tc.stream, nil)
			if !errors.Is(err, ErrTurnFailed) {
				t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
			}
			if err.Error() != want {
				t.Fatalf("error has %d runes, want the detail cut to %d runes ending in an ellipsis", len([]rune(err.Error())), bound)
			}
		})
	}
}

func TestRespondKeepsTheDetailAtItsEdges(t *testing.T) {
	const bound = 2000
	cases := []struct {
		name   string
		detail string
		want   string
	}{
		{name: "exactly the bound is kept", detail: strings.Repeat("é", bound), want: strings.Repeat("é", bound)},
		{name: "blanks around the bound are trimmed before the cut", detail: "  " + strings.Repeat("é", bound) + "  ", want: strings.Repeat("é", bound)},
	}
	sources := []struct {
		name   string
		stream func(detail string) string
	}{
		{name: "result text", stream: func(detail string) string { return errorResultLine(detail, `[]`) }},
		{name: "errors", stream: func(detail string) string { return errorResultLine("", fmt.Sprintf("[%q]", detail)) }},
		{name: "marked text", stream: func(detail string) string { return apiErrorStream(detail, "") }},
	}
	for _, source := range sources {
		for _, tc := range cases {
			t.Run(source.name+" "+tc.name, func(t *testing.T) {
				_, err := respondWithStream(t, source.stream(tc.detail), nil)
				if !errors.Is(err, ErrTurnFailed) {
					t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
				}
				if err.Error() != tc.want {
					t.Fatalf("error has %d runes, want %d intact runes", len([]rune(err.Error())), len([]rune(tc.want)))
				}
			})
		}
	}
}
