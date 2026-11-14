package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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
		got, _ := prunedResult(tc.result, tc.keep, 0)
		if got != tc.want {
			t.Fatalf("prunedResult(%q, %d) = %q, want %q", tc.result, tc.keep, got, tc.want)
		}
	}
}

func TestPrunedResultKeepsItsHeadAndItsTailAroundANote(t *testing.T) {
	cases := []struct {
		name     string
		result   string
		keep     int
		keepTail int
		want     string
		pruned   bool
	}{
		{name: "head and tail", result: "abcdefghij", keep: 3, keepTail: 2, want: "abc\n" + fmt.Sprintf(prunedResultNote, 5) + "\nij", pruned: true},
		{name: "no head", result: "abcdefghij", keep: 0, keepTail: 4, want: fmt.Sprintf(prunedResultNote, 6) + "\nghij", pruned: true},
		{name: "no tail", result: "abcdefghij", keep: 3, want: "abc\n" + fmt.Sprintf(prunedResultNote, 7), pruned: true},
		{name: "head and tail cover the whole", result: "abcdefghij", keep: 5, keepTail: 5, want: "abcdefghij"},
		{name: "head and tail overlap", result: "abcdefghij", keep: 6, keepTail: 6, want: "abcdefghij"},
		{name: "runes not bytes", result: "日本語テキスト", keep: 2, keepTail: 2, want: "日本\n" + fmt.Sprintf(prunedResultNote, 3) + "\nスト", pruned: true},
		{name: "empty result", result: "", keep: 0, keepTail: 0, want: ""},
		{name: "tail longer than the result", result: "abc", keep: 0, keepTail: 5, want: "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, pruned := prunedResult(tc.result, tc.keep, tc.keepTail)
			if got != tc.want || pruned != tc.pruned {
				t.Fatalf("prunedResult(%q, %d, %d) = %q, %v, want %q, %v", tc.result, tc.keep, tc.keepTail, got, pruned, tc.want, tc.pruned)
			}
		})
	}
}

func TestPruneAgainANoteKeepsNoFragmentOfIt(t *testing.T) {
	old, _ := prunedResult(strings.Repeat("abcdefghij", 30), 200, 0)
	want := strings.Repeat("abcdefghij", 10) + "\n" + fmt.Sprintf(prunedResultNote, 200)
	got, pruned := prunedResult(old, 100, 50)
	if got != want || !pruned {
		t.Fatalf("prunedResult(old, 100, 50) = %q, %v, want %q, true", got, pruned, want)
	}
	blocks, err := truncatedBlocks([]json.RawMessage{textBlockJSON(t, old)}, 100, 50)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	assertTextBlocks(t, blocks, want)
}

func TestPruneAgainStopsTheHeadBeforeAnEarlyNote(t *testing.T) {
	old := strings.Repeat("a", 10) + "\n" + fmt.Sprintf(prunedResultNote, 40) + "\n" + "bbbbb"
	want := strings.Repeat("a", 10) + "\n" + fmt.Sprintf(prunedResultNote, 43) + "\n" + "bb"
	got, pruned := prunedResult(old, 50, 2)
	if got != want || !pruned {
		t.Fatalf("prunedResult(old, 50, 2) = %q, %v, want %q, true", got, pruned, want)
	}
	if strings.Count(got, "[herrscher") != 1 {
		t.Fatalf("prunedResult(old, 50, 2) = %q, want a single note", got)
	}
}

func TestAlreadyPrunedNeedsTheSameHeadAndTail(t *testing.T) {
	pruned, _ := prunedResult("abcdefghij", 3, 2)
	cases := map[string]struct {
		keep, keepTail int
		want           bool
	}{
		"same head and tail":              {keep: 3, keepTail: 2, want: true},
		"looser head and tail":            {keep: 4, keepTail: 3, want: true},
		"tighter head":                    {keep: 2, keepTail: 2, want: false},
		"tighter tail":                    {keep: 3, keepTail: 1, want: false},
		"no tail asked where one is kept": {keep: 3, keepTail: 0, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := alreadyPruned(pruned, tc.keep, tc.keepTail); got != tc.want {
				t.Fatalf("alreadyPruned(%q, %d, %d) = %v, want %v", pruned, tc.keep, tc.keepTail, got, tc.want)
			}
		})
	}
}

func textBlockJSON(t *testing.T, text string) json.RawMessage {
	t.Helper()
	block, err := encodeJSON(textBlock{Type: blockText, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func assertTextBlocks(t *testing.T, blocks []json.RawMessage, want ...string) {
	t.Helper()
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %s", len(blocks), len(want), blocks)
	}
	for index, block := range blocks {
		if text, isText := blockOfText(block); !isText || text != want[index] {
			t.Fatalf("block %d = %s, want text %q", index, block, want[index])
		}
	}
}

func TestTruncatedBlocksKeepsTheHeadAndTheTailAcrossBlocks(t *testing.T) {
	blocks := []json.RawMessage{textBlockJSON(t, "aaaa"), textBlockJSON(t, "bbbb"), textBlockJSON(t, "cccc")}
	pruned, err := truncatedBlocks(blocks, 2, 2)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	assertTextBlocks(t, pruned, "aa\n"+fmt.Sprintf(prunedResultNote, 8), "cc")
}

func TestTruncatedBlocksKeepsNonTextBlocksBetweenTheHeadAndTheTail(t *testing.T) {
	blocks := []json.RawMessage{textBlockJSON(t, "first part"), json.RawMessage(imageBlock), json.RawMessage(referenceBlock), textBlockJSON(t, secondPart())}
	pruned, err := truncatedBlocks(blocks, 5, 4)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	if len(pruned) != 4 || string(pruned[1]) != imageBlock || string(pruned[2]) != referenceBlock {
		t.Fatalf("pruned blocks = %s, want the image and the reference kept between the head and the tail", pruned)
	}
	assertTextBlocks(t, []json.RawMessage{pruned[0], pruned[3]}, "first\n"+fmt.Sprintf(prunedResultNote, 102), "gain")
}

func TestTruncatedBlocksKeepsEverythingWhenTheHeadAndTheTailCoverIt(t *testing.T) {
	blocks := []json.RawMessage{textBlockJSON(t, "aaaa"), textBlockJSON(t, "bbbb"), textBlockJSON(t, "cccc")}
	pruned, err := truncatedBlocks(blocks, 6, 6)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	assertTextBlocks(t, pruned, "aaaa", "bbbb", "cccc")
}

func TestTruncatedBlocksKeepsABlockInsideTheTailAsItIs(t *testing.T) {
	tailBlock := json.RawMessage(`{"type": "text", "text": "\u0063cccc"}`)
	blocks := []json.RawMessage{textBlockJSON(t, "aaaa"), textBlockJSON(t, "bbbb"), tailBlock}
	pruned, err := truncatedBlocks(blocks, 2, 5)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	if len(pruned) != 2 || string(pruned[1]) != string(tailBlock) {
		t.Fatalf("pruned blocks = %s, want the tail block %s kept byte for byte", pruned, tailBlock)
	}
	assertTextBlocks(t, pruned[:1], "aa\n"+fmt.Sprintf(prunedResultNote, 6))
}

func TestTruncatedBlocksDropsAnEmptyTextBlockInTheCut(t *testing.T) {
	blocks := []json.RawMessage{textBlockJSON(t, "aaaa"), textBlockJSON(t, ""), textBlockJSON(t, "cccc")}
	pruned, err := truncatedBlocks(blocks, 2, 2)
	if err != nil {
		t.Fatalf("truncatedBlocks: %v", err)
	}
	assertTextBlocks(t, pruned, "aa\n"+fmt.Sprintf(prunedResultNote, 4), "cc")
}

func runesInHistory(history contracts.History) int {
	total := 0
	for _, call := range history.Calls() {
		total += len([]rune(call.Input)) + len([]rune(call.Result))
	}
	return total
}

func TestPruneResultKeepsTheHeadAndTheTailOfAResult(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	result := []rune(longResult())
	const keep, keepTail = 7, 10
	want := string(result[:keep]) + "\n" + fmt.Sprintf(prunedResultNote, len(result)-keep-keepTail) + "\n" + string(result[len(result)-keepTail:])
	outcome, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: keep, KeepTail: keepTail}})
	if err != nil || outcome.Edited != 1 {
		t.Fatalf("PruneHistory = %+v, %v, want one edit", outcome, err)
	}
	history, err := r.ReadHistory(t.Context())
	if err != nil || history.Calls()[0].Result != want {
		t.Fatalf("ReadHistory after prune = %+v, %v, want result %q", history.Calls()[0], err, want)
	}
	if _, statErr := os.Stat(path + transcriptBackupExt); statErr != nil {
		t.Fatalf("the prune wrote no backup: %v", statErr)
	}
}

func TestPruneResultWithATailCountsTheRunesOfTheHistory(t *testing.T) {
	fixture := newTranscriptCase(t)
	r := fixture.r
	before, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory before prune: %v", err)
	}
	result := []rune(longResult())
	const keep, keepTail = 7, 10
	note := []rune(fmt.Sprintf(prunedResultNote, len(result)-keep-keepTail))
	keptResult := keep + 1 + len(note) + 1 + keepTail
	outcome, err := r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: keep, KeepTail: keepTail}})
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	wantBefore := runesInHistory(before)
	wantAfter := wantBefore - len(result) + keptResult
	if outcome.CharsBefore != wantBefore || outcome.CharsAfter != wantAfter {
		t.Fatalf("PruneHistory outcome = %+v, want CharsBefore %d and CharsAfter %d", outcome, wantBefore, wantAfter)
	}
	after, err := r.ReadHistory(t.Context())
	if err != nil || runesInHistory(after) != outcome.CharsAfter {
		t.Fatalf("ReadHistory after prune counts %d runes, want %d: %v", runesInHistory(after), outcome.CharsAfter, err)
	}
}

func TestPruneResultWithATailTwiceEditsNothingTheSecondTime(t *testing.T) {
	fixture := newTranscriptCase(t)
	r, path := fixture.r, fixture.path
	edits := []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: 10, KeepTail: 5}}
	if _, err := r.PruneHistory(t.Context(), edits); err != nil {
		t.Fatalf("first PruneHistory: %v", err)
	}
	once, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.PruneHistory(t.Context(), edits)
	if err != nil || second.Edited != 0 || second.CharsAfter != second.CharsBefore {
		t.Fatalf("second PruneHistory = %+v, %v, want no edit", second, err)
	}
	twice, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(once, twice) {
		t.Fatalf("the second prune rewrote the transcript: %v", err)
	}
}

func TestPruneResultKeepingTheWholeResultEditsNothing(t *testing.T) {
	fixture := newTranscriptCase(t)
	length := len([]rune(longResult()))
	outcome, err := fixture.r.PruneHistory(t.Context(), []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneResult, KeepChars: length - 10, KeepTail: 10}})
	if err != nil || outcome.Edited != 0 {
		t.Fatalf("PruneHistory = %+v, %v, want no edit", outcome, err)
	}
	assertOnlyTheTranscript(t, fixture.path, transcriptFixture())
}
