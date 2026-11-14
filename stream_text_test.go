package claude

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	thinkingBlockLine = `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`
	blockToolLine     = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b1","name":"Read","input":{"file_path":"/x"}}]}}`
	blockResultLine   = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`
)

func textBlockLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, text)
}

func apiErrorTextLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]},"isApiErrorMessage":true,"session_id":"s"}`, text)
}

func turnTextDetails(t *testing.T, lines ...string) []string {
	t.Helper()
	canned := strings.Join(append(slices.Clone(lines), blockResultLine), "\n") + "\n"
	var texts []string
	_, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), func(e contracts.BackendEvent) {
		if e.Kind == "text" {
			texts = append(texts, e.Detail)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return texts
}

func assertTextDetails(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("textes émis = %q, attendu %q", got, want)
	}
}

func TestTextBlocksOfATurnAreSeparatedByAParagraphBreak(t *testing.T) {
	got := turnTextDetails(t, textBlockLine("Je regarde."), blockToolLine, textBlockLine("Voici le fichier."))
	assertTextDetails(t, got, []string{"Je regarde.", "\n\nVoici le fichier."})
}

func TestFirstTextBlockOfATurnHasNoSeparator(t *testing.T) {
	got := turnTextDetails(t, textBlockLine("Je regarde."))
	assertTextDetails(t, got, []string{"Je regarde."})
}

func TestBlankTextBlockDoesNotDoubleTheSeparator(t *testing.T) {
	got := turnTextDetails(t, textBlockLine("Je regarde."), textBlockLine(" \n "), textBlockLine("Voici."))
	assertTextDetails(t, got, []string{"Je regarde.", "\n\nVoici."})
}

func TestBlankTextBlockBeforeTheFirstTextDoesNotOpenTheSeparator(t *testing.T) {
	got := turnTextDetails(t, textBlockLine(" \n "), textBlockLine("Voici."))
	assertTextDetails(t, got, []string{"Voici."})
}

func TestParserStateDoesNotLeakAcrossTurns(t *testing.T) {
	first := turnTextDetails(t, textBlockLine("Premier tour."), blockToolLine, textBlockLine("Suite."))
	assertTextDetails(t, first, []string{"Premier tour.", "\n\nSuite."})
	second := turnTextDetails(t, textBlockLine("Second tour."))
	assertTextDetails(t, second, []string{"Second tour."})
}

func TestThinkingAndToolsDoNotPreventTheSeparator(t *testing.T) {
	got := turnTextDetails(t, textBlockLine("Je regarde."), thinkingBlockLine, blockToolLine, textBlockLine("Voici."))
	assertTextDetails(t, got, []string{"Je regarde.", "\n\nVoici."})
}

func TestThinkingAndToolsDoNotTriggerTheSeparator(t *testing.T) {
	got := turnTextDetails(t, thinkingBlockLine, blockToolLine, textBlockLine("Voici."))
	assertTextDetails(t, got, []string{"Voici."})
}

func TestAPIErrorBlocksAreNotCountedAsEmittedText(t *testing.T) {
	got := turnTextDetails(t, apiErrorTextLine("Request failed."), textBlockLine("Voici."))
	assertTextDetails(t, got, []string{"Voici."})
}

func TestFirstTextAfterAResetHasNoSeparator(t *testing.T) {
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	r.sess = newStreamSession(discardWriteCloser{}, strings.NewReader(textBlockLine("Je regarde.")+"\n"))
	r.spawn = func(string) (*streamSession, error) {
		reply := textBlockLine("Voici.") + "\n" + blockResultLine + "\n"
		return newStreamSession(discardWriteCloser{}, strings.NewReader(reply)), nil
	}
	var sequence, texts []string
	_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, func(e contracts.BackendEvent) {
		switch e.Kind {
		case "text":
			sequence = append(sequence, "text")
			texts = append(texts, e.Detail)
		case "reset":
			sequence = append(sequence, "reset")
		}
	})
	if err != nil {
		t.Fatalf("Respond after a reset: %v", err)
	}
	if want := []string{"text", "reset", "text"}; !slices.Equal(sequence, want) {
		t.Fatalf("events = %q, want %q", sequence, want)
	}
	assertTextDetails(t, texts, []string{"Je regarde.", "Voici."})
	mustClose(t, r)
}
