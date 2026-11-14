package claude

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func historyRunes(history contracts.History) int {
	total := 0
	for _, call := range history.Calls() {
		total += utf8.RuneCountInString(call.Input) + utf8.RuneCountInString(call.Result)
	}
	return total
}

func runeHead(text string, keep int) string {
	kept := 0
	for offset := range text {
		if kept == keep {
			return text[:offset]
		}
		kept++
	}
	return text
}

func runeTail(text string, keep int) string {
	if keep <= 0 {
		return ""
	}
	skip := max(0, utf8.RuneCountInString(text)-keep)
	seen := 0
	for offset := range text {
		if seen == skip {
			return text[offset:]
		}
		seen++
	}
	return ""
}

func textRunes(blocks []json.RawMessage) int {
	total := 0
	for _, block := range blocks {
		if text, isText := blockOfText(block); isText {
			total += utf8.RuneCountInString(text)
		}
	}
	return total
}

type notePlacement struct {
	first, last, runes, carried int
}

func placeNotes(texts []string, total int) notePlacement {
	placement := notePlacement{first: total}
	offset := 0
	for _, text := range texts {
		for _, span := range prunedResultSpan.FindAllStringSubmatchIndex(text, -1) {
			start := offset + utf8.RuneCountInString(text[:span[0]])
			end := offset + utf8.RuneCountInString(text[:span[1]])
			placement.carried += noteCount(text[span[2]:span[3]])
			placement.first = min(placement.first, start)
			placement.last = max(placement.last, end)
			placement.runes += end - start
		}
		offset += utf8.RuneCountInString(text)
	}
	return placement
}

type prunedCut struct {
	head, tailStart, removed, missing int
}

func planPrunedCut(texts []string, total, keep, keepTail int) prunedCut {
	placement := placeNotes(texts, total)
	head := min(keep, placement.first)
	tailStart := max(total-keepTail, placement.last, 0)
	removed := tailStart - head - placement.runes
	return prunedCut{head: head, tailStart: tailStart, removed: removed, missing: removed + placement.carried}
}

func noteCount(digits string) int {
	count, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return count
}

func prunedResult(result string, keep, keepTail int) (string, bool) {
	total := utf8.RuneCountInString(result)
	cut := planPrunedCut([]string{result}, total, keep, keepTail)
	if cut.removed <= 0 {
		return result, false
	}
	note := fmt.Sprintf(prunedResultNote, cut.missing)
	return joinText(joinText(runeHead(result, cut.head), note), runeTail(result, total-cut.tailStart)), true
}

func alreadyPruned(result string, keep, keepTail int) bool {
	if result == prunedCallNote {
		return true
	}
	note := prunedResultPattern.FindStringIndex(result)
	if note == nil {
		return false
	}
	head := strings.TrimSuffix(result[:note[0]], "\n")
	tail := strings.TrimPrefix(result[note[1]:], "\n")
	return utf8.RuneCountInString(head) <= keep && utf8.RuneCountInString(tail) <= keepTail
}
