package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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

type closingAppend struct {
	io.WriteCloser

	path string
	t    *testing.T
}

func (c *closingAppend) Close() error {
	appendToTranscript(c.t, c.path)
	return c.WriteCloser.Close()
}

func TestPruneHistoryKeepsWhatClaudeWritesWhileItCloses(t *testing.T) {
	fixture := newTranscriptCase(t)
	r := fixture.r
	r.sess = startShellSession(t, "sleep 60")
	r.sess.sessID = transcriptSessionID
	r.sess.stdin = &closingAppend{WriteCloser: r.sess.stdin, path: fixture.path, t: t}
	outcome, err := pruneWithin(t, r, []contracts.HistoryEdit{{CallID: "t1", Action: contracts.PruneCall}})
	if err != nil || outcome.Edited != 1 {
		t.Fatalf("PruneHistory = %+v, %v, want the call pruned after claude closed", outcome, err)
	}
	pruned, err := os.ReadFile(fixture.path)
	if err != nil || !strings.HasSuffix(string(pruned), appendedLine) {
		t.Fatalf("transcript = %q, %v, want the line claude wrote while closing kept", pruned, err)
	}
}

func TestRemoveStaleStagedKeepsBackupsAndFreshLinksOfAnOldTranscript(t *testing.T) {
	path := newTranscriptCase(t).path
	dir, name := filepath.Dir(path), filepath.Base(path)
	old := time.Now().Add(-2 * staleStagedAge)
	staleName := name + transcriptStagedExt + "." + strconv.FormatInt(old.Unix(), stagedTimeBase) + ".STALE"
	legacy := name + transcriptStagedExt
	for _, file := range []string{name + transcriptBackupExt, name + transcriptPreviousExt, staleName, legacy} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("kept or not\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{name, name + transcriptBackupExt, name + transcriptPreviousExt, staleName, legacy} {
		if err := os.Chtimes(filepath.Join(dir, file), old, old); err != nil {
			t.Fatal(err)
		}
	}
	freshLink := stagingName(name + transcriptPreviousExt)
	if err := os.Link(path, filepath.Join(dir, freshLink)); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, root)
	removeStaleStaged(root, name)
	for _, gone := range []string{staleName, legacy} {
		if _, statErr := os.Stat(filepath.Join(dir, gone)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: %v, want it removed", gone, statErr)
		}
	}
	for _, kept := range []string{name, name + transcriptBackupExt, name + transcriptPreviousExt, freshLink} {
		if _, statErr := os.Stat(filepath.Join(dir, kept)); statErr != nil {
			t.Fatalf("%s: %v, want it kept", kept, statErr)
		}
	}
}
