package claude

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func checkUnchanged(root *os.Root, name string, read os.FileInfo) error {
	current, err := root.Stat(name)
	if err != nil {
		return fmt.Errorf("stat claude transcript: %w", err)
	}
	if !os.SameFile(read, current) || read.Size() != current.Size() || !read.ModTime().Equal(current.ModTime()) {
		return fmt.Errorf("%w: %s", ErrTranscriptChanged, name)
	}
	return nil
}

func swapTranscript(root *os.Root, name, stagedName string, read os.FileInfo) error {
	backup, err := backUpTranscript(root, name)
	if err != nil {
		return err
	}
	if err = checkUnchanged(root, name, read); err != nil {
		removeStaged(root, backup.link)
		return err
	}
	if backup.replaces != "" {
		if err = root.Rename(backup.link, backup.replaces); err != nil {
			removeStaged(root, backup.link)
			return fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
		}
	}
	if err = root.Rename(stagedName, name); err != nil {
		return fmt.Errorf("replace claude transcript: %w", err)
	}
	if syncErr := syncDirectory(root); syncErr != nil {
		slog.Warn("claude transcript pruned, but its directory was not synced: a crash may bring the old transcript back", "transcript", name, "error", syncErr)
	}
	logKeptBackups(root, name)
	return nil
}

type transcriptBackup struct {
	link     string
	replaces string
}

func backUpTranscript(root *os.Root, name string) (transcriptBackup, error) {
	original := name + transcriptBackupExt
	err := root.Link(name, original)
	if err == nil {
		return transcriptBackup{link: original}, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return transcriptBackup{}, fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
	}
	previous := name + transcriptPreviousExt
	pending := stagingName(previous)
	if err = root.Link(name, pending); err != nil {
		return transcriptBackup{}, fmt.Errorf("%w: %w", ErrTranscriptBackup, err)
	}
	return transcriptBackup{link: pending, replaces: previous}, nil
}

func logKeptBackups(root *os.Root, name string) {
	var kept int64
	for _, backup := range []string{name + transcriptBackupExt, name + transcriptPreviousExt} {
		if info, err := root.Stat(backup); err == nil {
			kept += info.Size()
		}
	}
	slog.Info("claude transcript pruned, backups kept", "transcript", name, "backup_bytes", kept)
}

func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("sync claude transcript directory: %w", err)
	}
	if err = dir.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync claude transcript directory: %w", err), dir.Close())
	}
	if err = dir.Close(); err != nil {
		return fmt.Errorf("sync claude transcript directory: %w", err)
	}
	return nil
}

func closeAndLog(closer io.Closer) {
	if err := closer.Close(); err != nil {
		slog.Warn("closing a claude transcript", "error", err)
	}
}

func stagingName(name string) string {
	return name + transcriptStagedExt + "." + strconv.FormatInt(time.Now().Unix(), stagedTimeBase) + "." + rand.Text()
}

func removeStaleStaged(root *os.Root, name string) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		slog.Warn("listing staged claude transcripts", "error", err)
		return
	}
	for _, entry := range entries {
		for _, prefix := range []string{name + transcriptStagedExt, name + transcriptPreviousExt + transcriptStagedExt} {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if staged, known := stagedTime(entry, prefix); known && time.Since(staged) > staleStagedAge {
				removeStaged(root, entry.Name())
			}
			break
		}
	}
}

func stagedTime(entry fs.DirEntry, prefix string) (time.Time, bool) {
	stamp, _, named := strings.Cut(strings.TrimPrefix(entry.Name(), prefix+"."), ".")
	if seconds, err := strconv.ParseInt(stamp, stagedTimeBase, 64); named && err == nil {
		return time.Unix(seconds, 0), true
	}
	info, err := entry.Info()
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func removeStaged(root *os.Root, name string) {
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("removing a staged claude transcript", "name", name, "error", err)
	}
}
