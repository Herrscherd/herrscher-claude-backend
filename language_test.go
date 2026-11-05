package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const (
	successResult = `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	emptyFailure  = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","session_id":"s"}` + "\n"
)

func sentContents(t *testing.T, sent *sentLines) []string {
	t.Helper()
	var contents []string
	for _, raw := range bytes.Split(bytes.TrimSpace(sent.Bytes()), []byte("\n")) {
		var line struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("sent line is not a user message: %v: %q", err, raw)
		}
		contents = append(contents, line.Message.Content)
	}
	return contents
}

func respondAll(t *testing.T, r *streamResponder, prompts ...contracts.Prompt) {
	t.Helper()
	for _, prompt := range prompts {
		if _, err := r.Respond(context.Background(), prompt, nil); err != nil {
			t.Fatalf("Respond(%+v): %v", prompt, err)
		}
	}
}

func TestFrenchTurnCarriesTheLanguageDirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult)
	respondAll(t, r, contracts.Prompt{Content: "bonjour", Language: i18n.French})
	want := frenchDirective + "\n\nbonjour"
	if got := sentContents(t, sent); len(got) != 1 || got[0] != want {
		t.Fatalf("sent = %q, want [%q]", got, want)
	}
}

func TestTurnWithoutLanguageHasNoDirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult)
	respondAll(t, r, contracts.Prompt{Content: "hi"})
	if got := sentContents(t, sent); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("sent = %q, want [hi]", got)
	}
}

func TestTurnWithEnglishHasNoDirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult)
	respondAll(t, r, contracts.Prompt{Content: "hi", Language: i18n.English})
	if got := sentContents(t, sent); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("sent = %q, want [hi]", got)
	}
}

func TestLanguageChangeBetweenTurnsIsFollowed(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult+successResult+successResult)
	respondAll(t, r,
		contracts.Prompt{Content: "un", Language: i18n.French},
		contracts.Prompt{Content: "deux", Language: i18n.English},
		contracts.Prompt{Content: "trois", Language: i18n.French},
	)
	want := []string{frenchDirective + "\n\nun", "deux", frenchDirective + "\n\ntrois"}
	got := sentContents(t, sent)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent = %q, want %q", got, want)
	}
}

func TestUserContentIsSentUnaltered(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult)
	respondAll(t, r, contracts.Prompt{
		Content:     "look",
		Attachments: []string{"/tmp/a.png"},
		Language:    i18n.French,
	})
	want := frenchDirective + "\n\nlook\n\n[Attached image: /tmp/a.png]"
	if got := sentContents(t, sent); len(got) != 1 || got[0] != want {
		t.Fatalf("sent = %q, want [%q]", got, want)
	}
}

func TestFrenchTurnFailureIsWrittenInFrench(t *testing.T) {
	r, _ := compactingResponder(t.Context(), emptyFailure)
	_, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi", Language: i18n.French}, nil)
	if err == nil || err.Error() != "claude a signalé une erreur" {
		t.Fatalf("err = %v, want the French turn failure text", err)
	}
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
	}
}

func TestTurnFailureWithoutLanguageIsEnglish(t *testing.T) {
	r, _ := compactingResponder(t.Context(), emptyFailure)
	_, err := r.Respond(t.Context(), contracts.Prompt{Content: "hi"}, nil)
	if err == nil || err.Error() != "claude reported an error" {
		t.Fatalf("err = %v, want the English turn failure text", err)
	}
}

func TestRunCmdCarriesTheLanguageDirective(t *testing.T) {
	got, err := runCmd(context.Background(), "printf %s", nil, contracts.Prompt{Content: "look", Language: i18n.French})
	if err != nil {
		t.Fatal(err)
	}
	if want := frenchDirective + "\n\nlook"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}
