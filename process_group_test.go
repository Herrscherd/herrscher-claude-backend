//go:build unix

package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const descendantDeadline = 10 * time.Second

func fakeClaudeSpawning(t *testing.T, spawn string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\n" + spawn + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return script
}

func descendantSpawn(pidFile string) string {
	return "sleep 300 &\necho $! > " + pidFile
}

func escapedDescendantSpawn(pidFile string) string {
	return "setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 300' &"
}

func awaitDescendantPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(descendantDeadline)
	for {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				t.Cleanup(func() { killDescendant(t, pid) })
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid never written to %s: %v", pidFile, readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func killDescendant(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill descendant %d: %v", pid, err)
	}
}

func awaitDescendantGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(descendantDeadline)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %d survived the teardown", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requireSetsid(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
}

func TestStreamCloseKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeSpawning(t, descendantSpawn(pidFile))
	sess, err := startStreamSession(t.Context(), []string{"sh", script}, "", "", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("startStreamSession: %v", err)
	}
	t.Cleanup(func() {
		if err := sess.Close(); err != nil {
			t.Errorf("cleanup Close: %v", err)
		}
	})
	descendant := awaitDescendantPID(t, pidFile)
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	awaitDescendantGone(t, descendant)
}

func TestOneshotCancelKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeSpawning(t, descendantSpawn(pidFile))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := runCmdAsync(ctx, script)
	descendant := awaitDescendantPID(t, pidFile)
	cancel()
	awaitRunCmdReturn(t, done)
	awaitDescendantGone(t, descendant)
}

func TestOneshotCancelReturnsWhenDescendantEscapesGroup(t *testing.T) {
	t.Parallel()
	requireSetsid(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeSpawning(t, escapedDescendantSpawn(pidFile))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := runCmdAsync(ctx, script)
	awaitDescendantPID(t, pidFile)
	cancel()
	awaitRunCmdReturn(t, done)
}

func runCmdAsync(ctx context.Context, script string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := runCmd(ctx, "sh "+script, nil, contracts.Prompt{})
		done <- err
	}()
	return done
}

func awaitRunCmdReturn(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled runCmd reported success")
		}
	case <-time.After(descendantDeadline):
		t.Fatal("runCmd still blocked after its context was cancelled")
	}
}
