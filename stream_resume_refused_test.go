package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	deadBeforeAnyLineCLI = `log=$1; shift; echo "$*" >> "$log"; case "$*" in *--resume*) exit 1;; esac; read line; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`
	acceptingResumeCLI   = `log=$1; shift; echo "$*" >> "$log"; read line; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-init"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-init"}'`
)

func fakeCLIBase(log, script string) []string {
	return []string{"sh", "-c", script, "claude", log}
}

func spawnedArgvs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read spawn log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestLostResumeBeforeAnyLineStartsAFreshSession(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, deadBeforeAnyLineCLI), "", "sid-init", "", nil)

	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil)
	if err != nil {
		t.Fatalf("the turn after a resume lost before any line: %v: a never-accepted resume must fall back to a fresh session", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 2 {
		t.Fatalf("the CLI was spawned %d times, want 2: the refused resume, then one fresh start", len(argvs))
	}
	if !strings.Contains(argvs[0], "--resume sid-init") {
		t.Fatalf("first spawn args = %q, want --resume sid-init", argvs[0])
	}
	if strings.Contains(argvs[1], "--resume") {
		t.Fatalf("fallback spawn args = %q, want no --resume: the dead id must not be sent again", argvs[1])
	}
	if got := r.ResumeToken(); got != "sid-new" {
		t.Fatalf("ResumeToken() = %q, want %q: the fresh session's id replaces the dead one", got, "sid-new")
	}
	mustClose(t, r)
}

func TestAcceptedResumeKeepsTheResumeWithoutFallback(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, acceptingResumeCLI), "", "sid-init", "", nil)

	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "again"}, nil)
	if err != nil {
		t.Fatalf("resumed turn: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 1 || !strings.Contains(argvs[0], "--resume sid-init") {
		t.Fatalf("spawns = %q, want exactly one spawn with --resume sid-init", argvs)
	}
	if got := r.ResumeToken(); got != "sid-init" {
		t.Fatalf("ResumeToken() = %q, want %q", got, "sid-init")
	}
	mustClose(t, r)
}
