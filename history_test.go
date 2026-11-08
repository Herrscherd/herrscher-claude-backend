package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const transcriptSessionID = "4f1c2b3a-0000-4000-8000-000000000001"

func longInput() string { return strings.Repeat("<x>", 100) }

func longResult() string {
	return "package parser, with a body that is long" + strings.Repeat(" and longer", 10)
}

func transcriptFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"before the boundary"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"tool_use","id":"old","name":"Read","input":{"file_path":"a"}}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"old result"}]},"uuid":"u1","parentUuid":"a0"}`,
		`{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","uuid":"b0","parentUuid":null}`,
		`{"type":"user","message":{"role":"user","content":"fix the parser"},"uuid":"u2","parentUuid":"b0","isCompactSummary":false}`,
		`{"type":"attachment","attachment":{"type":"environment"},"uuid":"x0","parentUuid":"u2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},"uuid":"a1","parentUuid":"x0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reading the file"}]},"uuid":"a2","parentUuid":"a1"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"parser.go"}}]},"uuid":"a3","parentUuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"out.go","content":"` + longInput() + `"}}]},"uuid":"a4","parentUuid":"a3"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + longResult() + `"}]},"uuid":"u3","parentUuid":"a3","toolUseResult":{"kept":"as is"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"written"}],"is_error":true}]},"uuid":"u4","parentUuid":"a4"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a5","parentUuid":"u4"}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"side","name":"Grep","input":{}}]},"uuid":"s0","parentUuid":"a5"}`,
	}, "\n") + "\n"
}

type transcriptCase struct {
	r    *streamResponder
	path string
}

func newTranscriptCase(t *testing.T) transcriptCase {
	t.Helper()
	return newTranscriptCaseWith(t, transcriptFixture())
}

func newTranscriptCaseWith(t *testing.T, transcript string) transcriptCase {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "-work-repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
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
	if calls[1].Result != "written" || strings.Contains(calls[1].Input, longInput()) || !strings.Contains(calls[1].Input, `"file_path":"out.go"`) || strings.Contains(calls[1].Input, `\u003c`) {
		t.Fatalf("pruned call = %+v, want its short result kept and its input shortened without HTML escapes", calls[1])
	}
	backup, err := os.ReadFile(path + transcriptBackupExt)
	if err != nil || string(backup) != transcriptFixture() {
		t.Fatalf("backup = %q, %v, want the transcript before the prune", backup, err)
	}
	assertUntouchedLines(t, path, transcriptFixture(), "u0", "a0", "u1", "b0", "u2", "x0", "a1", "a2", "a5", "s0")
	assertKeyOrderKept(t, path)
}

func assertKeyOrderKept(t *testing.T, path string) {
	t.Helper()
	pruned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package\n[herrscher pruned`,
		`characters]"}]},"uuid":"u3","parentUuid":"a3","toolUseResult":{"kept":"as is"}}` + "\n",
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Write","input":{`,
		`}}]},"uuid":"a4","parentUuid":"a3"}` + "\n",
	} {
		if !bytes.Contains(pruned, []byte(want)) {
			t.Fatalf("pruned transcript lacks %q, want only message.content spliced into the raw line", want)
		}
	}
}

func TestPruneResultTwiceEditsNothingTheSecondTime(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	edits := []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: 10}}
	first, err := r.PruneHistory(t.Context(), edits)
	if err != nil || first.Edited != 1 || first.CharsAfter >= first.CharsBefore {
		t.Fatalf("first PruneHistory = %+v, %v, want one edit and fewer runes", first, err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.PruneHistory(t.Context(), edits)
	if err != nil || second.Edited != 0 || second.CharsAfter != second.CharsBefore {
		t.Fatalf("second PruneHistory = %+v, %v, want no edit", second, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("the second prune rewrote the transcript: %v", err)
	}
	if _, statErr := os.Stat(path + transcriptPreviousExt); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the second prune wrote a backup: %v", statErr)
	}
}

func TestPruneResultNeverGrowsAShortResult(t *testing.T) {
	fixture := newTranscriptCase(t)
	outcome, err := fixture.r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t2", Action: contracts.PruneResult, KeepChars: 1}})
	if err != nil || outcome.Edited != 0 || outcome.CharsAfter != outcome.CharsBefore {
		t.Fatalf("PruneHistory = %+v, %v, want no edit", outcome, err)
	}
	assertOnlyTheTranscript(t, fixture.path, transcriptFixture())
}

func assertUntouchedLines(t *testing.T, path, original string, uuids ...string) {
	t.Helper()
	pruned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, uuid := range uuids {
		for line := range strings.SplitSeq(original, "\n") {
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

type transcriptProbe struct {
	io.WriteCloser

	path             string
	untouchedAtClose bool
}

func (p *transcriptProbe) Close() error {
	transcript, err := os.ReadFile(p.path)
	p.untouchedAtClose = err == nil && string(transcript) == transcriptFixture()
	return p.WriteCloser.Close()
}

func TestPruneHistoryKillsTheLiveSessionBeforeWritingSoTheNextTurnResumes(t *testing.T) {
	fixture := newTranscriptCase(t)
	r := fixture.r
	r.sess = startShellSession(t, "sleep 60")
	r.sess.sessID = transcriptSessionID
	probe := &transcriptProbe{WriteCloser: r.sess.stdin, path: fixture.path}
	r.sess.stdin = probe
	claude := r.sess.cmd
	if _, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}}); err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if !probe.untouchedAtClose {
		t.Fatal("the transcript was rewritten before the live claude was closed")
	}
	if claude.ProcessState == nil || claude.ProcessState.Success() {
		t.Fatalf("the live claude ended as %v, want it killed and reaped", claude.ProcessState)
	}
	if r.sess != nil || r.currentSessionID() != transcriptSessionID {
		t.Fatalf("after a prune the session is %v with id %q, want none and the id kept for --resume", r.sess, r.currentSessionID())
	}
}

func TestPruneHistoryFailingAfterTheSessionDropKeepsTheIDForResume(t *testing.T) {
	path := newTranscriptCase(t).path
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	r := newStreamResponder(t.Context(), nil, "", "", "", map[string]string{configDirVar: root})
	r.sess = startShellSession(t, "sleep 60")
	r.sess.sessID = transcriptSessionID
	if err := os.MkdirAll(filepath.Join(path+transcriptStagedExt, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}}); err == nil {
		t.Fatal("PruneHistory succeeded with its staging path taken")
	}
	if r.sess != nil || r.currentSessionID() != transcriptSessionID {
		t.Fatalf("after a failed prune the session is %v with id %q, want none and the id kept for --resume", r.sess, r.currentSessionID())
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != transcriptFixture() {
		t.Fatalf("transcript = %q, %v, want it untouched", got, err)
	}
	history, err := r.ReadHistory(t.Context())
	if err != nil || len(history.Calls()) != 2 {
		t.Fatalf("ReadHistory after a failed prune = %+v, %v, want the session read through its kept id", history, err)
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

const (
	imageBlock     = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	referenceBlock = `{"type":"tool_reference","tool_name":"Grep"}`
)

func secondPart() string { return "second part" + strings.Repeat(" again", 15) }

func mixedResultFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"look at the screen"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"tool_use","id":"shot","name":"Screenshot","input":{}}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"shot","content":[{"type":"text","text":"first part"},` + imageBlock + `,` + referenceBlock + `,{"type":"text","text":"` + secondPart() + `"}]}]},"uuid":"u1","parentUuid":"a0"}`,
	}, "\n") + "\n"
}

func prunedResultBlocks(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(transcript)), "\n")
	var entry struct {
		Message struct {
			Content []struct {
				Content []json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err = json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil || len(entry.Message.Content) != 1 {
		t.Fatalf("pruned result line does not decode: %v", err)
	}
	return entry.Message.Content[0].Content
}

func assertPrunedBlocks(t *testing.T, blocks []json.RawMessage, wantText string) {
	t.Helper()
	if len(blocks) != 3 || string(blocks[1]) != imageBlock || string(blocks[2]) != referenceBlock {
		t.Fatalf("pruned blocks = %s, want one text block then the image and the reference untouched", blocks)
	}
	if text, isText := blockOfText(blocks[0]); !isText || text != wantText {
		t.Fatalf("pruned text block = %s, want %q", blocks[0], wantText)
	}
}

func TestPruneHistoryRewritesOnlyTheTextBlocksOfAResult(t *testing.T) {
	cases := map[string]struct {
		edit contracts.HistoryEdit
		want string
	}{
		"prune_result": {
			edit: contracts.HistoryEdit{CallID: "shot", Action: contracts.PruneResult, KeepChars: 5},
			want: "first\n" + fmt.Sprintf(prunedResultNote, 106),
		},
		"prune_call": {
			edit: contracts.HistoryEdit{CallID: "shot", Action: contracts.PruneCall},
			want: prunedCallNote,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newTranscriptCaseWith(t, mixedResultFixture())
			if _, err := fixture.r.PruneHistory(t.Context(), []contracts.HistoryEdit{tc.edit}); err != nil {
				t.Fatalf("PruneHistory: %v", err)
			}
			assertPrunedBlocks(t, prunedResultBlocks(t, fixture.path), tc.want)
			history, err := fixture.r.ReadHistory(t.Context())
			if err != nil || history.Calls()[0].Result != tc.want {
				t.Fatalf("ReadHistory after prune = %+v, %v, want result %q", history, err, tc.want)
			}
		})
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

func keptOutput() string { return "kept output" + strings.Repeat(" line", 20) }

func rewoundFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"start"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":"do X"},"uuid":"u1","parentUuid":"a0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"y1","name":"Bash","input":{"command":"` + longInput() + `"}}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"y1","content":"abandoned output"}]},"uuid":"u2","parentUuid":"a1"}`,
		`{"type":"user","message":{"role":"user","content":"do Y instead"},"uuid":"u3","parentUuid":"a0"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"y1","name":"Bash","input":{"command":"` + longInput() + `"}}]},"uuid":"a2","parentUuid":"u3"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"y1","content":"` + keptOutput() + `"}]},"uuid":"u4","parentUuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a3","parentUuid":"u4"}`,
	}, "\n") + "\n"
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
	if _, err = fixture.r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "y1", Action: contracts.PruneCall}}); err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	assertUntouchedLines(t, fixture.path, rewoundFixture(), "u0", "a0", "u1", "a1", "u2", "u3", "a3")
	pruned, err := os.ReadFile(fixture.path)
	if err != nil || bytes.Contains(pruned, []byte(keptOutput())) {
		t.Fatalf("the result on the branch of the leaf was not pruned: %v", err)
	}
}

const appendedLine = `{"type":"user","message":{"role":"user","content":"typed in a terminal"},"uuid":"u9","parentUuid":"a5"}` + "\n"

func appendToTranscript(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteString(t, file, appendedLine)
	mustClose(t, file)
}

func assertOnlyTheTranscript(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("transcript = %q, %v, want it untouched", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("transcript directory holds %v, %v, want the transcript alone", entries, err)
	}
}

func TestPruneTranscriptRefusesAFileAppendedSinceItWasRead(t *testing.T) {
	path := newTranscriptCase(t).path
	read, err := readTranscript(t.Context(), transcriptFile{dir: filepath.Dir(path), name: filepath.Base(path)}, map[string]bool{"t1": true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planEdits(read, []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}})
	if err != nil {
		t.Fatal(err)
	}
	appendToTranscript(t, path)
	if err = pruneTranscript(t.Context(), read, plan); !errors.Is(err, ErrTranscriptChanged) {
		t.Fatalf("pruneTranscript error = %v, want ErrTranscriptChanged", err)
	}
	assertOnlyTheTranscript(t, path, transcriptFixture()+appendedLine)
}

func TestSwapTranscriptRefusesAFileAppendedRightBeforeTheRename(t *testing.T) {
	path := newTranscriptCase(t).path
	dir, name := filepath.Dir(path), filepath.Base(path)
	read, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stagedName := name + transcriptStagedExt
	if err = os.WriteFile(filepath.Join(dir, stagedName), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendToTranscript(t, path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, root)
	if err = swapTranscript(root, name, stagedName, read); !errors.Is(err, ErrTranscriptChanged) {
		t.Fatalf("swapTranscript error = %v, want ErrTranscriptChanged", err)
	}
	if err = os.Remove(filepath.Join(dir, stagedName)); err != nil {
		t.Fatalf("the staged file is gone after a refused swap: %v", err)
	}
	assertOnlyTheTranscript(t, path, transcriptFixture()+appendedLine)
}

func TestTranscriptLoopsStopOnACancelledContext(t *testing.T) {
	path := newTranscriptCase(t).path
	transcript := transcriptFile{dir: filepath.Dir(path), name: filepath.Base(path)}
	read, err := readTranscript(t.Context(), transcript, map[string]bool{"t1": true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planEdits(read, []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = readTranscript(cancelled, transcript, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("readTranscript error = %v, want context.Canceled", err)
	}
	if err = pruneTranscript(cancelled, read, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("pruneTranscript error = %v, want context.Canceled", err)
	}
	assertOnlyTheTranscript(t, path, transcriptFixture())
}

func TestLaterPrunesKeepTheOriginalAndReplaceOnePreviousBackup(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	rounds := [][]contracts.HistoryEdit{
		{{CallID: "t1", Action: contracts.PruneResult, KeepChars: 50}},
		{{CallID: "t2", Action: contracts.PruneCall}},
		{{CallID: "t1", Action: contracts.PruneCall}},
	}
	var beforeLast []byte
	for index, edits := range rounds {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeLast = current
		if outcome, pruneErr := r.PruneHistory(t.Context(), edits); pruneErr != nil || outcome.Edited != 1 {
			t.Fatalf("prune %d = %+v, %v, want one edit", index, outcome, pruneErr)
		}
	}
	if original, err := os.ReadFile(path + transcriptBackupExt); err != nil || string(original) != transcriptFixture() {
		t.Fatalf("original backup = %q, %v, want the transcript before any prune", original, err)
	}
	if previous, err := os.ReadFile(path + transcriptPreviousExt); err != nil || !bytes.Equal(previous, beforeLast) {
		t.Fatalf("previous backup = %q, %v, want the transcript before the last prune", previous, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 3 {
		t.Fatalf("transcript directory holds %v, %v, want the transcript and its two backups", entries, err)
	}
}
