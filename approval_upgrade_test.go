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

func removeFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
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
	if got := r.launchState(); got != launchBound {
		t.Fatalf("launch state after the upgrade = %v, want bound", got)
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
	if got := r.launchState(); got != launchBare {
		t.Fatalf("launch state after the refusal = %v, want bare", got)
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
