package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const transcriptSessionID = "4f1c2b3a-0000-4000-8000-000000000001"

func longInput() string { return strings.Repeat("x", 300) }

func transcriptFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"before the boundary"},"uuid":"u0"}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"tool_use","id":"old","name":"Read","input":{"file_path":"a"}}]},"uuid":"a0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"old result"}]},"uuid":"u1"}`,
		`{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","uuid":"b0"}`,
		`{"type":"user","message":{"role":"user","content":"fix the parser"},"uuid":"u2","isCompactSummary":false}`,
		`{"type":"attachment","attachment":{"type":"environment"},"uuid":"x0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},"uuid":"a1"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reading the file"}]},"uuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"parser.go"}}]},"uuid":"a3"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"out.go","content":"` + longInput() + `"}}]},"uuid":"a4"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package parser, with a body that is long"}]},"uuid":"u3","toolUseResult":{"kept":"as is"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"written"}],"is_error":true}]},"uuid":"u4"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a5"}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"side","name":"Grep","input":{}}]},"uuid":"s0"}`,
	}, "\n") + "\n"
}

type transcriptCase struct {
	r    *streamResponder
	path string
}

func newTranscriptCase(t *testing.T) transcriptCase {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "-work-repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(transcriptFixture()), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptCase{
		r:    newStreamResponder(t.Context(), nil, "", transcriptSessionID, "", map[string]string{configDirVar: root}),
		path: path,
	}
}

func TestReadHistoryReturnsTheMainChainAfterTheLastCompaction(t *testing.T) {
	r := newTranscriptCase(t).r
	history, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	want := []contracts.HistoryMessage{
		{Role: contracts.RoleUser, Text: "fix the parser"},
		{Role: contracts.RoleAssistant, Text: "reading the file", Calls: []contracts.HistoryCall{
			{ID: "t1", Tool: "Read", Input: `{"file_path":"parser.go"}`, Result: "package parser, with a body that is long"},
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

func TestPruneHistoryEditsResultsInPlaceAndKeepsABackup(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	edits := []contracts.HistoryEdit{
		{CallID: "t1", Action: contracts.PruneResult, KeepChars: 7},
		{CallID: "t2", Action: contracts.PruneCall},
	}
	outcome, err := r.PruneHistory(t.Context(), edits)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if outcome.Edited != 2 || outcome.CharsAfter >= outcome.CharsBefore {
		t.Fatalf("PruneHistory outcome = %+v, want 2 edits and fewer runes", outcome)
	}
	history, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory after prune: %v", err)
	}
	calls := history.Calls()
	if !strings.HasPrefix(calls[0].Result, "package\n[herrscher pruned the rest of this result: ") {
		t.Fatalf("pruned result = %q, want its 7 first runes and a note", calls[0].Result)
	}
	if calls[1].Result != prunedCallNote || strings.Contains(calls[1].Input, longInput()) || !strings.Contains(calls[1].Input, `"file_path":"out.go"`) {
		t.Fatalf("pruned call = %+v, want the note and a shortened input", calls[1])
	}
	backup, err := os.ReadFile(path + transcriptBackupExt)
	if err != nil || string(backup) != transcriptFixture() {
		t.Fatalf("backup = %q, %v, want the transcript before the prune", backup, err)
	}
	assertUntouchedLines(t, path, "u0", "a0", "u1", "b0", "u2", "x0", "a1", "a2", "a5", "s0")
}

func assertUntouchedLines(t *testing.T, path string, uuids ...string) {
	t.Helper()
	pruned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, uuid := range uuids {
		for line := range strings.SplitSeq(transcriptFixture(), "\n") {
			if strings.Contains(line, `"uuid":"`+uuid+`"`) && !bytes.Contains(pruned, []byte(line+"\n")) {
				t.Fatalf("line %s changed, want it byte for byte", uuid)
			}
		}
	}
}

func TestPruneHistoryRefusesACallOutsideTheHistory(t *testing.T) {
	for _, callID := range []string{"old", "side", "missing"} {
		t.Run(callID, func(t *testing.T) {
			fixture := newTranscriptCase(t)
			r, path := fixture.r, fixture.path
			_, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{
				{CallID: "t1", Action: contracts.PruneCall},
				{CallID: callID, Action: contracts.PruneCall},
			})
			if !errors.Is(err, contracts.ErrUnknownHistoryCall) {
				t.Fatalf("PruneHistory(%s) error = %v, want ErrUnknownHistoryCall", callID, err)
			}
			if after, readErr := os.ReadFile(path); readErr != nil || string(after) != transcriptFixture() {
				t.Fatalf("transcript changed after a refused prune: %v", readErr)
			}
		})
	}
}

func TestPruneHistoryRefusesMalformedEdits(t *testing.T) {
	r := newTranscriptCase(t).r
	_, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1"}})
	if !errors.Is(err, contracts.ErrInvalidHistoryEdit) {
		t.Fatalf("PruneHistory error = %v, want ErrInvalidHistoryEdit", err)
	}
}

func TestPruneHistoryDropsTheLiveSessionSoTheNextTurnResumes(t *testing.T) {
	r := newTranscriptCase(t).r
	r.sess = newStreamSession(&sentLines{}, strings.NewReader(""))
	r.sess.sessID = transcriptSessionID
	if _, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}}); err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if r.sess != nil || r.currentSessionID() != transcriptSessionID {
		t.Fatalf("after a prune the session is %v with id %q, want none and the id kept for --resume", r.sess, r.currentSessionID())
	}
}

func TestPruneHistoryKeepsTheLiveSessionWhenNothingChanges(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	r.sess = newStreamSession(&sentLines{}, strings.NewReader(""))
	outcome, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: 1000}})
	if err != nil || outcome.Edited != 0 || outcome.CharsAfter != outcome.CharsBefore {
		t.Fatalf("PruneHistory = %+v, %v, want no edit", outcome, err)
	}
	if r.sess == nil {
		t.Fatal("a prune that changed nothing dropped the live session")
	}
	if _, statErr := os.Stat(path + transcriptBackupExt); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a prune that changed nothing wrote a backup: %v", statErr)
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

func TestPrunedResultCountsRunesNotBytes(t *testing.T) {
	cases := []struct {
		result string
		keep   int
		want   string
	}{
		{result: "héllo wörld", keep: 5, want: "héllo\n" + fmt.Sprintf(prunedResultNote, 6)},
		{result: "日本語", keep: 3, want: "日本語"},
		{result: "日本語", keep: 0, want: fmt.Sprintf(prunedResultNote, 3)},
	}
	for _, tc := range cases {
		got, _ := prunedResult(tc.result, tc.keep)
		if got != tc.want {
			t.Fatalf("prunedResult(%q, %d) = %q, want %q", tc.result, tc.keep, got, tc.want)
		}
	}
}
