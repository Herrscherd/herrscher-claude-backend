package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrTranscriptNotFound reports a session whose transcript file the claude CLI
// left in no project directory of its config directory.
var ErrTranscriptNotFound = errors.New("claude session transcript not found")

// ErrBadSessionID reports a session id that is not one the claude CLI writes, and
// so never names a transcript file.
var ErrBadSessionID = errors.New("claude session id is malformed")

// ErrTranscriptChanged reports a transcript that another writer, such as a
// `claude --resume` run in a terminal on the same session, changed after it was
// read: PruneHistory leaves it untouched rather than drop what was appended. The
// file is checked when the prune starts and again right before the rename, so
// only a write landing between that last check and the rename escapes it. A
// refused prune also removes the backup link it made, so a transcript never has
// more than its .herrscher-bak and its .herrscher-bak.prev next to it.
var ErrTranscriptChanged = errors.New("claude session transcript changed while being pruned")

// ErrTranscriptBackup reports a prune that could not keep the transcript it
// replaces, most often on a file system without hard links such as exFAT, vfat
// or some FUSE and SMB mounts: PruneHistory leaves the transcript untouched, and
// every later prune of that session fails the same way until it moves to a file
// system with hard links.
var ErrTranscriptBackup = errors.New("claude session transcript could not be backed up")

const (
	configDirVar          = "CLAUDE_CONFIG_DIR"
	transcriptBackupExt   = ".herrscher-bak"
	transcriptPreviousExt = ".herrscher-bak.prev"
	transcriptStagedExt   = ".herrscher-tmp"
	staleStagedAge        = time.Hour
	stagedTimeBase        = 36
	prunedResultNote      = "[herrscher pruned the rest of this result: %d characters]"
	prunedCallNote        = "[herrscher pruned the result of this call]"
	prunedInputRunes      = 200
)

var sessionIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,127}$`)

var prunedResultPattern = regexp.MustCompile(strings.Replace(regexp.QuoteMeta(prunedResultNote), "%d", `(\d+)`, 1))

var prunedResultSpan = regexp.MustCompile(`\n?` + prunedResultPattern.String() + `\n?`)

func configDir(env map[string]string) (string, error) {
	if dir := env[configDirVar]; dir != "" {
		return dir, nil
	}
	if dir := os.Getenv(configDirVar); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude config directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

type transcriptFile struct {
	dir  string
	name string
}

func transcriptPath(configRoot, sessionID string) (transcriptFile, error) {
	if !sessionIDPattern.MatchString(sessionID) {
		return transcriptFile{}, fmt.Errorf("%w: %q", ErrBadSessionID, sessionID)
	}
	name := sessionID + ".jsonl"
	projects := filepath.Join(configRoot, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return transcriptFile{}, fmt.Errorf("claude transcript lookup: %w", err)
	}
	newest, newestTime := "", int64(0)
	for _, entry := range entries {
		candidate := filepath.Join(projects, entry.Name(), name)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if stamp := info.ModTime().UnixNano(); newest == "" || stamp > newestTime {
			newest, newestTime = candidate, stamp
		}
	}
	if newest == "" {
		return transcriptFile{}, fmt.Errorf("%w: session %s under %s", ErrTranscriptNotFound, sessionID, configRoot)
	}
	return transcriptFile{dir: filepath.Dir(newest), name: name}, nil
}
