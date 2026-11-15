package claude

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestReadHistoryReturnsTheMainChainAfterTheLastCompaction(t *testing.T) {
	r := newTranscriptCase(t).r
	history, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	want := []contracts.HistoryMessage{
		{Role: contracts.RoleUser, Text: "fix the parser"},
		{Role: contracts.RoleAssistant, Text: "reading the file", Calls: []contracts.HistoryCall{
			{ID: "t1", Tool: "Read", Input: `{"file_path":"parser.go"}`, Result: longResult()},
			{ID: "t2", Tool: "Write", Input: `{"file_path":"out.go","content":"` + longInput() + `"}`, Result: "written", ResultIsError: true},
		}},
		{Role: contracts.RoleAssistant, Text: "done"},
	}
	if len(history.Messages) != len(want) {
		t.Fatalf("ReadHistory returned %d messages, want %d: %+v", len(history.Messages), len(want), history.Messages)
	}
	for index := range want {
		got, expected := history.Messages[index], want[index]
		if got.Role != expected.Role || got.Text != expected.Text || len(got.Calls) != len(expected.Calls) {
			t.Fatalf("message %d = %+v, want %+v", index, got, expected)
		}
		for callIndex := range expected.Calls {
			if got.Calls[callIndex] != expected.Calls[callIndex] {
				t.Fatalf("message %d call %d = %+v, want %+v", index, callIndex, got.Calls[callIndex], expected.Calls[callIndex])
			}
		}
	}
}

func TestAssistantTextBlocksOfATurnAreSeparatedByAParagraphBreak(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"go"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Je regarde."}]},"uuid":"a1","parentUuid":"u0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a"}}]},"uuid":"a2","parentUuid":"a1"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":" \n "}]},"uuid":"a3","parentUuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Voici le fichier."}]},"uuid":"a4","parentUuid":"a3"}`,
	}, "\n") + "\n"
	history, err := newTranscriptCaseWith(t, transcript).r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("ReadHistory returned %d messages, want 2: %+v", len(history.Messages), history.Messages)
	}
	if got, want := history.Messages[1].Text, "Je regarde.\n\nVoici le fichier."; got != want {
		t.Fatalf("assistant text = %q, want %q", got, want)
	}
}

func TestAssistantTextBlocksAreTrimmedLikeTheStream(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"go"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"\n  Je regarde.\n"}]},"uuid":"a1","parentUuid":"u0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a"}}]},"uuid":"a2","parentUuid":"a1"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Voici."}]},"uuid":"a3","parentUuid":"a2"}`,
	}, "\n") + "\n"
	history, err := newTranscriptCaseWith(t, transcript).r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("ReadHistory returned %d messages, want 2: %+v", len(history.Messages), history.Messages)
	}
	if got, want := history.Messages[1].Text, "Je regarde.\n\nVoici."; got != want {
		t.Fatalf("assistant text = %q, want %q", got, want)
	}
}

func TestReadHistoryLeavesOutAnAPIErrorEntryAndKeepsTheRealReply(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"go"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","isApiErrorMessage":true,"message":{"id":"err","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: refused"}]},"uuid":"a1","parentUuid":"u0"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Voici."}]},"uuid":"a2","parentUuid":"a1"}`,
	}, "\n") + "\n"
	history, err := newTranscriptCaseWith(t, transcript).r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	want := []contracts.HistoryMessage{
		{Role: contracts.RoleUser, Text: "go"},
		{Role: contracts.RoleAssistant, Text: "Voici."},
	}
	if len(history.Messages) != len(want) {
		t.Fatalf("ReadHistory returned %d messages, want %d: %+v", len(history.Messages), len(want), history.Messages)
	}
	for index := range want {
		if got := history.Messages[index]; got.Role != want[index].Role || got.Text != want[index].Text {
			t.Fatalf("message %d = %+v, want %+v", index, got, want[index])
		}
	}
}

func TestReadHistoryLeavesNoTraceOfAnAPIErrorEntry(t *testing.T) {
	userGo := `{"type":"user","message":{"role":"user","content":"go"},"uuid":"u0","parentUuid":null}`
	apiError := `{"type":"assistant","isApiErrorMessage":true,"message":{"id":"err","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: refused"}]},"uuid":"a1","parentUuid":"u0"}`
	userAgain := `{"type":"user","message":{"role":"user","content":"again"},"uuid":"u1","parentUuid":"a1"}`
	cases := []struct {
		name  string
		lines []string
		want  []contracts.HistoryMessage
	}{
		{
			name:  "between two user messages",
			lines: []string{userGo, apiError, userAgain},
			want:  []contracts.HistoryMessage{{Role: contracts.RoleUser, Text: "go"}, {Role: contracts.RoleUser, Text: "again"}},
		},
		{
			name:  "as the leaf of the chain",
			lines: []string{userGo, apiError},
			want:  []contracts.HistoryMessage{{Role: contracts.RoleUser, Text: "go"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transcript := strings.Join(tc.lines, "\n") + "\n"
			history, err := newTranscriptCaseWith(t, transcript).r.ReadHistory(t.Context())
			if err != nil {
				t.Fatalf("ReadHistory: %v", err)
			}
			if len(history.Messages) != len(tc.want) {
				t.Fatalf("ReadHistory returned %d messages, want %d: %+v", len(history.Messages), len(tc.want), history.Messages)
			}
			for index := range tc.want {
				if got := history.Messages[index]; got.Role != tc.want[index].Role || got.Text != tc.want[index].Text {
					t.Fatalf("message %d = %+v, want %+v", index, got, tc.want[index])
				}
			}
		})
	}
}

func TestReadHistoryWithoutASessionIsEmpty(t *testing.T) {
	r := newStreamResponder(t.Context(), nil, "", "", "", map[string]string{configDirVar: t.TempDir()})
	history, err := r.ReadHistory(t.Context())
	if err != nil || len(history.Messages) != 0 {
		t.Fatalf("ReadHistory = %+v, %v, want an empty history", history, err)
	}
}

func TestReadHistoryReportsAMissingOrMalformedSession(t *testing.T) {
	cases := map[string]error{
		"4f1c2b3a-0000-4000-8000-00000000ffff": ErrTranscriptNotFound,
		"../../etc/passwd":                     ErrBadSessionID,
		"*":                                    ErrBadSessionID,
	}
	for sessionID, want := range cases {
		r := newStreamResponder(context.Background(), nil, "", sessionID, "", map[string]string{configDirVar: t.TempDir()})
		if _, err := r.ReadHistory(t.Context()); !errors.Is(err, want) {
			t.Fatalf("ReadHistory(%q) error = %v, want %v", sessionID, err, want)
		}
	}
}

func TestReadHistoryFindsATranscriptUnderAConfigDirWithGlobCharacters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "conf[ig]*?")
	dir := filepath.Join(root, "projects", "-work-[repo]")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, transcriptSessionID+".jsonl"), []byte(transcriptFixture()), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newStreamResponder(t.Context(), nil, "", transcriptSessionID, "", map[string]string{configDirVar: root})
	history, err := r.ReadHistory(t.Context())
	if err != nil || len(history.Messages) == 0 {
		t.Fatalf("ReadHistory = %+v, %v, want the transcript found despite the brackets", history, err)
	}
}

func TestReadHistoryKeepsOneMessageAcrossAResultBetweenItsBlocks(t *testing.T) {
	r := newTranscriptCaseWith(t, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"read both"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"one"}]},"uuid":"u1","parentUuid":"a0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"two"}]},"uuid":"u2","parentUuid":"a1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"now the third"}]},"uuid":"u3","parentUuid":"u2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"ok"}]},"uuid":"a2","parentUuid":"u3"}`,
	}, "\n")+"\n").r
	history, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history.Messages) != 4 || len(history.Messages[1].Calls) != 2 || history.Messages[1].Calls[1].Result != "two" || history.Messages[3].Text != "ok" {
		t.Fatalf("ReadHistory = %+v, want the two calls of m1 in one message and the user text ending it", history.Messages)
	}
}

func TestReadHistoryFollowsTheBranchOfTheLeafAfterARewind(t *testing.T) {
	fixture := newTranscriptCaseWith(t, rewoundFixture())
	history, err := fixture.r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	var texts []string
	for _, message := range history.Messages {
		texts = append(texts, message.Text)
	}
	if strings.Join(texts, "|") != "start|hello|do Y instead||done" || history.Calls()[0].Result != keptOutput() {
		t.Fatalf("ReadHistory = %+v, want only the branch of the leaf", history.Messages)
	}
	if _, err = pruneWithin(t, fixture.r, []contracts.HistoryEdit{{CallID: "y1", Action: contracts.PruneCall}}); err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	assertUntouchedLines(t, fixture.path, rewoundFixture(), "u0", "a0", "u1", "a1", "u2", "u3", "a3")
	pruned, err := os.ReadFile(fixture.path)
	if err != nil || bytes.Contains(pruned, []byte(keptOutput())) {
		t.Fatalf("the result on the branch of the leaf was not pruned: %v", err)
	}
}
