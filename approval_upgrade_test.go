package claude

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func writeFakeCLI(tb testing.TB, path string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+approvalTurnScript+"\n"), 0o700); err != nil {
		tb.Fatalf("write fake CLI: %v", err)
	}
}

func startCompact(ctx context.Context, r *streamResponder) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := r.Compact(ctx, contracts.CompactRequest{}, nil)
		done <- err
	}()
	return done
}

func longTempRoot(tb testing.TB) string {
	tb.Helper()
	deep := filepath.Join(tb.TempDir(), strings.Repeat("d", 120))
	if err := os.Mkdir(deep, 0o700); err != nil {
		tb.Fatalf("create long temp root: %v", err)
	}
	return deep
}

func removeFiles(tb testing.TB, paths ...string) {
	tb.Helper()
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			tb.Fatalf("remove %s: %v", path, err)
		}
	}
}

func TestFailedLaunchLeavesLaterAskerTurnUsable(t *testing.T) {
	env := approvalEnv(t)
	cli := filepath.Join(t.TempDir(), "fake-cli")
	r := newStreamResponder(t.Context(), []string{cli}, "", "", t.TempDir(), env)
	t.Cleanup(func() { mustClose(t, r) })

	if _, err := respondWithin(t.Context(), t, r, contracts.Prompt{Content: "launch without an asker"}, nil); err == nil {
		t.Fatal("turn with a missing CLI succeeded")
	}
	writeFakeCLI(t, cli)
	releaseTurn(t, env, 1)

	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	done := startTurn(ctx, r, "with an asker")
	if got := awaitRecord(t, env["RECORD"]); got == "unset" {
		t.Fatal("CLI launched after the failed launch saw no approval socket")
	}
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("turn with an asker after a failed launch: %v", outcome.err)
	}
}

func TestCompactWithAskerOpensApprovalSocket(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startCompact(ctx, r)
	path := awaitRecord(t, env["RECORD"])
	answer := askHook(t, path, "compact-1")
	if !answer.Allowed() || answer.ID != "compact-1" {
		t.Fatalf("hook answer during compaction = %+v, want allow for compact-1", answer)
	}
	releaseTurn(t, env, 1)
	if err := awaitWithin(t, done, "the compaction"); err != nil {
		t.Fatalf("compact: %v", err)
	}
}

func TestBareSessionUpgradesWithResumeOnAskerTurn(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "first, without an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	removeFiles(t, env["RECORD"], env["ARGS"], env["GATE"]+"1")

	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	done := startTurn(ctx, r, "second, with an asker")
	path := awaitRecord(t, env["RECORD"])
	if args := awaitRecord(t, env["ARGS"]); !strings.Contains(args, "--resume s1") {
		t.Fatalf("relaunched CLI args = %q, want --resume s1", args)
	}
	answer := askHook(t, path, "req-up")
	if !answer.Allowed() || answer.ID != "req-up" {
		t.Fatalf("hook answer on the relaunched session = %+v, want allow for req-up", answer)
	}
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("second turn: %v", outcome.err)
	}
	if !sessionBound(r) {
		t.Fatal("session after the upgrade is not bound to the relay")
	}
}

func TestBareSessionWithoutResumeIDRefusesAskerTurn(t *testing.T) {
	env := approvalEnv(t)
	env["SESSION"] = ""
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "first, without an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}

	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	outcome := awaitTurn(t, startTurn(ctx, r, "second, with an asker"))
	if !errors.Is(outcome.err, ErrApprovalUnreachable) {
		t.Fatalf("second turn err = %v, want ErrApprovalUnreachable", outcome.err)
	}
	if r.currentSession() == nil {
		t.Fatal("refused turn closed the bare session")
	}
	if sessionBound(r) {
		t.Fatal("refused turn bound the bare session")
	}
}

func TestRelayOpenFailureKeepsCLIUnlaunched(t *testing.T) {
	env := approvalEnv(t)
	t.Setenv("TMPDIR", longTempRoot(t))
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	_, err := respondWithin(ctx, t, r, contracts.Prompt{Content: "run the tool"}, nil)
	if !errors.Is(err, errApprovalSocketUnbound) {
		t.Fatalf("turn err = %v, want errApprovalSocketUnbound", err)
	}
	if _, statErr := os.Stat(env["RECORD"]); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("CLI launched although its socket never bound: stat err = %v", statErr)
	}
}

func TestRelayForConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("TMPDIR", root)
	r := newStreamResponder(t.Context(), []string{"sh", "-c", "true"}, "", "", workDir, nil)
	t.Cleanup(func() { mustClose(t, r) })

	const callers = 8
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		relays []*approvalRelay
	)
	for range callers {
		wg.Go(func() {
			relay, err := r.approvalRelayFor(t.Context())
			if err != nil {
				t.Errorf("approvalRelayFor: %v", err)
				return
			}
			mu.Lock()
			relays = append(relays, relay)
			mu.Unlock()
		})
	}
	returnWithin(t, "concurrent approvalRelayFor calls", func() bool {
		wg.Wait()
		return true
	})

	if len(relays) != callers {
		t.Fatalf("got %d relays, want %d", len(relays), callers)
	}
	for _, relay := range relays {
		if relay != relays[0] {
			t.Fatal("concurrent calls returned different relays")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read temp root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("surplus relays left %d approval directories, want 1", len(entries))
	}
}

func sessionBound(r *streamResponder) bool {
	sess := r.currentSession()
	return sess != nil && sess.relay != nil
}

func TestRelayOpenFailureLeavesBareSessionAlive(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "first, without an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	bare := r.currentSession()
	if bare == nil || sessionBound(r) {
		t.Fatal("first turn did not leave a bare session")
	}
	removeFiles(t, env["ARGS"])

	t.Setenv("TMPDIR", longTempRoot(t))
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	if _, err := respondWithin(ctx, t, r, contracts.Prompt{Content: "second, with an asker"}, nil); !errors.Is(err, errApprovalSocketUnbound) {
		t.Fatalf("turn err = %v, want errApprovalSocketUnbound", err)
	}
	if r.currentSession() != bare {
		t.Fatal("failed relay open replaced the bare session")
	}

	releaseTurn(t, env, 2)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "third, without an asker")); outcome.err != nil {
		t.Fatalf("third turn: %v", outcome.err)
	}
	if _, err := os.Stat(env["ARGS"]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bare session was relaunched: stat ARGS err = %v", err)
	}
	if r.currentSession() != bare {
		t.Fatal("next turn without an asker replaced the bare session")
	}
}

func TestResumeIDWithDashIsRefusedOnUpgrade(t *testing.T) {
	env := approvalEnv(t)
	env["SESSION"] = "--foo"
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "first, without an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}

	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	outcome := awaitTurn(t, startTurn(ctx, r, "second, with an asker"))
	if !errors.Is(outcome.err, ErrApprovalUnreachable) {
		t.Fatalf("second turn err = %v, want ErrApprovalUnreachable", outcome.err)
	}
	if r.currentSession() == nil || sessionBound(r) {
		t.Fatal("refused upgrade touched the bare session")
	}
}

func TestInvalidResumeIDIsNeverPassed(t *testing.T) {
	env := approvalEnv(t)
	r := newStreamResponder(t.Context(), []string{"sh", "-c", approvalTurnScript}, "", "--foo", t.TempDir(), env)
	t.Cleanup(func() { mustClose(t, r) })
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(t.Context(), r, "run without an asker")); outcome.err != nil {
		t.Fatalf("turn: %v", outcome.err)
	}
	data, err := os.ReadFile(env["ARGS"])
	if err != nil {
		t.Fatalf("read CLI args: %v", err)
	}
	if strings.Contains(string(data), "--resume") || strings.Contains(string(data), "--foo") {
		t.Fatalf("CLI args = %q, want no --resume for an invalid id", data)
	}
}

func TestCompactWithoutAskerOnBoundSessionDeniesRequests(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(ctx, r, "first, with an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	if !sessionBound(r) {
		t.Fatal("asker turn did not bind the session")
	}
	removeFiles(t, env["RECORD"])

	done := startCompact(t.Context(), r)
	path := awaitRecord(t, env["RECORD"])
	answer := askHook(t, path, "compact-req")
	if answer.Decision != contracts.DecisionDeny || answer.ID != "compact-req" {
		t.Fatalf("hook answer during a compaction without asker = %+v, want deny keeping compact-req", answer)
	}
	releaseTurn(t, env, 2)
	if err := awaitWithin(t, done, "the compaction"); err != nil {
		t.Fatalf("compact without asker on a bound session: %v", err)
	}
}

func TestClosedRelayIsReplacedOnNextAskerTurn(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(ctx, r, "first, with an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	firstPath := awaitRecord(t, env["RECORD"])
	old := r.approvals
	if err := returnWithin(t, "close the relay", old.close); err != nil {
		t.Fatalf("close relay: %v", err)
	}
	removeFiles(t, env["RECORD"], env["ARGS"], env["GATE"]+"1")

	done := startTurn(ctx, r, "second, after the relay stopped")
	secondPath := awaitRecord(t, env["RECORD"])
	if secondPath == firstPath {
		t.Fatal("stopped relay was not replaced")
	}
	if args := awaitRecord(t, env["ARGS"]); !strings.Contains(args, "--resume s1") {
		t.Fatalf("relaunched CLI args = %q, want --resume s1", args)
	}
	answer := askHook(t, secondPath, "req-replaced")
	if !answer.Allowed() || answer.ID != "req-replaced" {
		t.Fatalf("hook answer on the replacement relay = %+v, want allow for req-replaced", answer)
	}
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("second turn: %v", outcome.err)
	}
	if r.approvals == old || r.approvals == nil {
		t.Fatal("responder still holds the stopped relay")
	}
}
