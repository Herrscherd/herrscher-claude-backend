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

const (
	descendantDeadline = 10 * time.Second
	descendantArgs     = "sleep 300"
)

type cmdResult struct {
	out string
	err error
}

type probeResult struct {
	report contracts.ProbeReport
	ok     bool
}

func fakeClaudeScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o600); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return script
}

func spawnAndExit(pidFile string) string {
	return "sleep 300 &\necho $! > " + pidFile
}

func spawnAndWait(pidFile string) string {
	return spawnAndExit(pidFile) + "\nwait"
}

func escapedSpawnAndWait(pidFile string) string {
	return "setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 300' &\nwait"
}

func awaitDescendantPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(descendantDeadline)
	for {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				t.Cleanup(func() { killOwnedDescendant(t, pid) })
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid never written to %s: %v", pidFile, readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func descendantRunning(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-o", "stat=,args=", "-p", strconv.Itoa(pid)).Output()
	if _, exited := errors.AsType[*exec.ExitError](err); exited {
		return false
	}
	if err != nil {
		t.Fatalf("ps for descendant %d: %v", pid, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return false
	}
	isZombie := strings.HasPrefix(fields[0], "Z")
	return !isZombie && strings.Join(fields[1:], " ") == descendantArgs
}

func killOwnedDescendant(t *testing.T, pid int) {
	t.Helper()
	if !descendantRunning(t, pid) {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill descendant %d: %v", pid, err)
	}
}

func awaitDescendantGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(descendantDeadline)
	for descendantRunning(t, pid) {
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

func closeWithin(t *testing.T, sess *streamSession) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- sess.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(descendantDeadline):
		t.Fatal("Close did not return within 10 seconds")
		return nil
	}
}

func runCmdAsync(ctx context.Context, script string) <-chan cmdResult {
	done := make(chan cmdResult, 1)
	go func() {
		out, err := runCmd(ctx, "sh "+script, nil, contracts.Prompt{})
		done <- cmdResult{out: out, err: err}
	}()
	return done
}

func awaitRunCmd(t *testing.T, done <-chan cmdResult) cmdResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(descendantDeadline):
		t.Fatal("runCmd did not return within 10 seconds")
		return cmdResult{}
	}
}

func runProbeAsync(ctx context.Context, script string) <-chan probeResult {
	done := make(chan probeResult, 1)
	go func() {
		var into map[string]any
		report, ok := runProbeCommand(ctx, nil, &into, "sh", script)
		done <- probeResult{report: report, ok: ok}
	}()
	return done
}

func awaitProbe(t *testing.T, done <-chan probeResult) probeResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(descendantDeadline):
		t.Fatal("runProbeCommand did not return within 10 seconds")
		return probeResult{}
	}
}

func TestStreamCloseKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, spawnAndWait(pidFile))
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
	if err := closeWithin(t, sess); err != nil {
		t.Fatalf("Close: %v", err)
	}
	awaitDescendantGone(t, descendant)
}

func TestOneshotCancelKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, spawnAndWait(pidFile))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := runCmdAsync(ctx, script)
	descendant := awaitDescendantPID(t, pidFile)
	cancel()
	if res := awaitRunCmd(t, done); res.err == nil {
		t.Fatal("cancelled runCmd reported success")
	}
	awaitDescendantGone(t, descendant)
}

func TestOneshotCancelReturnsWhenDescendantEscapesGroup(t *testing.T) {
	t.Parallel()
	requireSetsid(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, escapedSpawnAndWait(pidFile))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := runCmdAsync(ctx, script)
	awaitDescendantPID(t, pidFile)
	cancel()
	if res := awaitRunCmd(t, done); res.err == nil {
		t.Fatal("cancelled runCmd reported success")
	}
}

func TestOneshotNormalExitKillsDescendantAndKeepsOutput(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, spawnAndExit(pidFile)+"\necho answer")
	done := runCmdAsync(t.Context(), script)
	descendant := awaitDescendantPID(t, pidFile)
	res := awaitRunCmd(t, done)
	if res.err != nil {
		t.Fatalf("runCmd after a normal exit: %v", res.err)
	}
	if res.out != "answer\n" {
		t.Fatalf("output = %q, want the answer", res.out)
	}
	awaitDescendantGone(t, descendant)
}

func TestProbeCancelKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, spawnAndWait(pidFile))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := runProbeAsync(ctx, script)
	descendant := awaitDescendantPID(t, pidFile)
	cancel()
	if res := awaitProbe(t, done); res.ok {
		t.Fatal("cancelled probe reported a readable report")
	}
	awaitDescendantGone(t, descendant)
}

func TestProbeNormalExitKillsDescendants(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	script := fakeClaudeScript(t, spawnAndExit(pidFile)+"\necho '{}'")
	done := runProbeAsync(t.Context(), script)
	descendant := awaitDescendantPID(t, pidFile)
	res := awaitProbe(t, done)
	if !res.ok {
		t.Fatal("probe after a normal exit did not read its report")
	}
	awaitDescendantGone(t, descendant)
}
