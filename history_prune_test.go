package claude

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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

func TestPruneHistoryKeepsTheAPIErrorLineByteForByteBesideAPrunedResult(t *testing.T) {
	const errorLine = `{"type":"assistant","isApiErrorMessage":true,"message":{"id":"err","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: refused"}]},"uuid":"a1","parentUuid":"u0"}`
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: "string content", content: `"` + longResult() + `"`},
		{name: "block content", content: `[{"type":"text","text":"` + longResult() + `"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcript := strings.Join([]string{
				`{"type":"user","message":{"role":"user","content":"go"},"uuid":"u0","parentUuid":null}`,
				errorLine,
				`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"parser.go"}}]},"uuid":"a2","parentUuid":"a1"}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + tc.content + `}]},"uuid":"u3","parentUuid":"a2"}`,
				`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Voici."}]},"uuid":"a4","parentUuid":"u3"}`,
			}, "\n") + "\n"
			fixture := newTranscriptCaseWith(t, transcript)
			r, path := fixture.r, fixture.path
			outcome, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: 7}})
			if err != nil || outcome.Edited != 1 {
				t.Fatalf("PruneHistory = %+v, %v, want one edit", outcome, err)
			}
			pruned, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !containsLine(pruned, errorLine) {
				t.Fatalf("pruned transcript lacks the API error line byte for byte:\n%s", pruned)
			}
			want := "package\n" + fmt.Sprintf(prunedResultNote, utf8.RuneCountInString(longResult())-7)
			if got := resultOfCall(t, r, "t1"); got != want {
				t.Fatalf("pruned result of t1 = %q, want %q", got, want)
			}
		})
	}
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
	if err := os.WriteFile(path+transcriptBackupExt, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path+transcriptPreviousExt, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}}); !errors.Is(err, ErrTranscriptBackup) {
		t.Fatalf("PruneHistory error = %v, want ErrTranscriptBackup with its previous backup path taken", err)
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 3 {
		t.Fatalf("after a refused backup the directory holds %d entries, %v, want no staged file left", len(entries), err)
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

func TestPruneHistoryRefusesAKeepTailOnAPrunedCall(t *testing.T) {
	fixture := newTranscriptCase(t)
	_, err := fixture.r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall, KeepTail: 3}})
	if !errors.Is(err, contracts.ErrInvalidHistoryEdit) {
		t.Fatalf("PruneHistory error = %v, want ErrInvalidHistoryEdit", err)
	}
	assertOnlyTheTranscript(t, fixture.path, transcriptFixture())
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

func TestPruneResultRecognisesOnlyAPrunedShape(t *testing.T) {
	note := fmt.Sprintf(prunedResultNote, 3)
	cases := []struct {
		name           string
		text           string
		keep, keepTail int
		wantEdited     int
	}{
		{name: "looks pruned but is cut", text: strings.Repeat("y", 300) + "\n" + note, keep: 10, wantEdited: 1},
		{name: "cited note in a short text is taken as pruned", text: "ab\n" + note + "\ncd", keep: 2, keepTail: 2, wantEdited: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transcript := strings.Replace(transcriptFixture(), longResult(), strings.ReplaceAll(tc.text, "\n", `\n`), 1)
			fixture := newTranscriptCaseWith(t, transcript)
			edits := []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: tc.keep, KeepTail: tc.keepTail}}
			first, err := fixture.r.PruneHistory(t.Context(), edits)
			if err != nil || first.Edited != tc.wantEdited {
				t.Fatalf("PruneHistory = %+v, %v, want %d edited", first, err, tc.wantEdited)
			}
			second, err := fixture.r.PruneHistory(t.Context(), edits)
			if err != nil || second.Edited != 0 {
				t.Fatalf("second PruneHistory = %+v, %v, want no edit", second, err)
			}
		})
	}
}
