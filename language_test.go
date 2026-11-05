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

func TestTurnWithEnglishCarriesEnglishDirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult)
	respondAll(t, r, contracts.Prompt{Content: "hi", Language: i18n.English})
	want := englishDirective + "\n\nhi"
	if got := sentContents(t, sent); len(got) != 1 || got[0] != want {
		t.Fatalf("sent = %q, want [%q]", got, want)
	}
}

func TestFrenchThenEnglishTurnCarriesEnglishDirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult+successResult)
	respondAll(t, r,
		contracts.Prompt{Content: "bonjour", Language: i18n.French},
		contracts.Prompt{Content: "hello", Language: i18n.English},
	)
	want := []string{frenchDirective + "\n\nbonjour", englishDirective + "\n\nhello"}
	got := sentContents(t, sent)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent = %q, want %q", got, want)
	}
}

func TestLanguageChangeBetweenTurnsIsFollowed(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult+successResult+successResult)
	respondAll(t, r,
		contracts.Prompt{Content: "un", Language: i18n.French},
		contracts.Prompt{Content: "deux", Language: i18n.English},
		contracts.Prompt{Content: "trois", Language: i18n.French},
	)
	want := []string{frenchDirective + "\n\nun", englishDirective + "\n\ndeux", frenchDirective + "\n\ntrois"}
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

func TestLanguageDirectiveSkipsSlashCommands(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"/compact", "/compact"},
		{"/ma-skill arg", "/ma-skill arg"},
		{"  /compact", "  /compact"},
		{"bonjour", frenchDirective + "\n\nbonjour"},
	} {
		t.Run(tc.content, func(t *testing.T) {
			r, sent := compactingResponder(t.Context(), successResult)
			respondAll(t, r, contracts.Prompt{Content: tc.content, Language: i18n.French})
			if got := sentContents(t, sent); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("sent = %q, want [%q]", got, tc.want)
			}
		})
	}
}

func TestRunCmdLeavesSlashCommandsIntactInFrench(t *testing.T) {
	got, err := runCmd(context.Background(), "printf %s", nil, contracts.Prompt{Content: "/ma-skill arg", Language: i18n.French})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/ma-skill arg" {
		t.Fatalf("prompt = %q, want the slash command untouched", got)
	}
}

func TestCompactionNeverReceivesADirective(t *testing.T) {
	r, sent := compactingResponder(t.Context(), successResult+successResult)
	respondAll(t, r, contracts.Prompt{Content: "bonjour", Language: i18n.French})
	if _, err := r.Compact(t.Context(), contracts.CompactRequest{}, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got := sentContents(t, sent)
	if len(got) != 2 || got[1] != "/compact" {
		t.Fatalf("sent = %q, want the compaction line to be exactly /compact", got)
	}
}
