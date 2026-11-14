package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func prunedCall(call contracts.HistoryCall, edit pruneEdit) contracts.HistoryCall {
	if edit.input != "" {
		call.Input = edit.input
	}
	if edit.content != nil {
		call.Result = resultText(edit.content)
	}
	return call
}

func pruneOutcome(history contracts.History, edits map[string]pruneEdit) contracts.PruneOutcome {
	pruned := contracts.History{Messages: make([]contracts.HistoryMessage, len(history.Messages))}
	edited := 0
	for index, message := range history.Messages {
		message.Calls = append([]contracts.HistoryCall(nil), message.Calls...)
		for callIndex, call := range message.Calls {
			edit, ok := edits[call.ID]
			if !ok {
				continue
			}
			if pruned := prunedCall(call, edit); pruned != call {
				message.Calls[callIndex] = pruned
				edited++
			}
		}
		pruned.Messages[index] = message
	}
	return contracts.PruneOutcome{Edited: edited, CharsBefore: historyRunes(history), CharsAfter: historyRunes(pruned)}
}

func plannedInput(edit contracts.HistoryEdit, input string) string {
	if edit.Action != contracts.PruneCall {
		return ""
	}
	if pruned := prunedInput(input); utf8.RuneCountInString(pruned) < utf8.RuneCountInString(input) {
		return pruned
	}
	return ""
}

func planEdits(read *historyBuilder, edits []contracts.HistoryEdit) (map[string]pruneEdit, error) {
	if err := contracts.ValidateHistoryEdits(edits); err != nil {
		return nil, err
	}
	inputs := make(map[string]string)
	for _, call := range read.history.Calls() {
		inputs[call.ID] = call.Input
	}
	plan := make(map[string]pruneEdit, len(edits))
	for _, edit := range edits {
		input, known := inputs[edit.CallID]
		if !known {
			return nil, fmt.Errorf("%w: %s", contracts.ErrUnknownHistoryCall, edit.CallID)
		}
		content, err := prunedContent(edit, read.resultContent[edit.CallID])
		if err != nil {
			return nil, err
		}
		plan[edit.CallID] = pruneEdit{HistoryEdit: edit, input: plannedInput(edit, input), content: content}
	}
	return plan, nil
}

func pruneTranscript(ctx context.Context, read *historyBuilder, plan map[string]pruneEdit) error {
	root, err := os.OpenRoot(read.file.dir)
	if err != nil {
		return fmt.Errorf("open claude transcript directory: %w", err)
	}
	defer closeAndLog(root)
	source, err := root.Open(read.file.name)
	if err != nil {
		return fmt.Errorf("open claude transcript: %w", err)
	}
	defer closeAndLog(source)
	if changedErr := checkUnchanged(root, read.file.name, read.read); changedErr != nil {
		return changedErr
	}
	removeStaleStaged(root, read.file.name)
	stagedName := stagingName(read.file.name)
	staged, err := root.OpenFile(stagedName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, read.read.Mode().Perm())
	if err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	defer removeStaged(root, stagedName)
	if err = writePrunedTranscript(ctx, io.NewSectionReader(source, 0, read.read.Size()), staged, read.onChain, plan); err != nil {
		return errors.Join(err, staged.Close())
	}
	if err = staged.Sync(); err != nil {
		return errors.Join(fmt.Errorf("stage claude transcript: %w", err), staged.Close())
	}
	if err = staged.Close(); err != nil {
		return fmt.Errorf("stage claude transcript: %w", err)
	}
	return swapTranscript(root, read.file.name, stagedName, read.read)
}

func writePrunedTranscript(ctx context.Context, source io.Reader, target io.Writer, onChain map[int]bool, plan map[string]pruneEdit) error {
	writer := bufio.NewWriter(target)
	err := eachTranscriptLine(ctx, source, func(lineIndex int, line []byte) error {
		if onChain[lineIndex] {
			pruned, pruneErr := prunedLine(line, plan)
			if pruneErr != nil {
				return fmt.Errorf("claude transcript at line %d: %w", lineIndex, pruneErr)
			}
			line = pruned
		}
		if _, writeErr := writer.Write(line); writeErr != nil {
			return fmt.Errorf("write claude transcript: %w", writeErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = writer.Flush(); err != nil {
		return fmt.Errorf("write claude transcript: %w", err)
	}
	return nil
}
