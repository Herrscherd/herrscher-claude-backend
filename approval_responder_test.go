package claude

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/approvalsock"
)

const approvalTestWait = 10 * time.Second

const approvalTurnScript = `n=0
while read -r _; do
	n=$((n+1))
	printf '%s' "${HERRSCHER_APPROVAL_SOCKET-unset}" > "$RECORD"
	while [ ! -f "$GATE$n" ]; do sleep 0.01; done
	printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}' '{"type":"result","is_error":false,"result":"ok","session_id":"s1"}'
done`

type turnOutcome struct {
	text string
	err  error
}

func approvalEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{"RECORD": filepath.Join(dir, "record"), "GATE": filepath.Join(dir, "gate")}
}

func newApprovalResponder(t *testing.T, env map[string]string) *streamResponder {
	t.Helper()
	r := newStreamResponder(t.Context(), []string{"sh", "-c", approvalTurnScript}, "", "", t.TempDir(), env)
	t.Cleanup(func() {
		if err := returnWithin(t, "Close", r.Close); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r
}

func startTurn(ctx context.Context, r *streamResponder, text string) <-chan turnOutcome {
	done := make(chan turnOutcome, 1)
	go func() {
		out, err := r.Respond(ctx, contracts.Prompt{Content: text}, nil)
		done <- turnOutcome{text: out, err: err}
	}()
	return done
}

func releaseTurn(t *testing.T, env map[string]string, turn int) {
	t.Helper()
	if err := os.WriteFile(env["GATE"]+strconv.Itoa(turn), nil, 0o600); err != nil {
		t.Fatalf("release turn %d: %v", turn, err)
	}
}

func awaitRecord(t *testing.T, path string) string {
	t.Helper()
	return returnWithin(t, "the CLI to record its environment", func() string {
		for {
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				return string(data)
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func awaitTurn(t *testing.T, done <-chan turnOutcome) turnOutcome {
	t.Helper()
	return awaitWithin(t, done, "the turn")
}

func askHook(t *testing.T, path, id string) contracts.ApprovalAnswer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), approvalTestWait)
	defer cancel()
	answer, err := approvalsock.Ask(ctx, path, contracts.ApprovalRequest{ID: id, Tool: "Bash", Subject: "ls"})
	if err != nil {
		t.Fatalf("Ask %s: %v", id, err)
	}
	return answer
}

func allowingAsker() contracts.ApprovalAsker {
	return func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow, Reason: "from the daemon"}
	}
}

func TestApprovalAllowReachesHookDuringTurn(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "run the tool")
	path := awaitRecord(t, env["RECORD"])
	answer := askHook(t, path, "req-1")
	if !answer.Allowed() || answer.ID != "req-1" {
		t.Fatalf("hook answer = %+v, want allow for req-1", answer)
	}
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("turn: %v", outcome.err)
	}
}

func TestApprovalDeniedOutsideTurn(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "first")
	path := awaitRecord(t, env["RECORD"])
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("turn: %v", outcome.err)
	}

	answer := askHook(t, path, "req-9")
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-9" {
		t.Fatalf("answer outside a turn = %+v, want deny keeping req-9", answer)
	}
}

func TestApprovalDeniedOnTurnWithoutAsker(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "first")
	path := awaitRecord(t, env["RECORD"])
	releaseTurn(t, env, 1)
	awaitTurn(t, done)

	if err := os.Remove(env["RECORD"]); err != nil {
		t.Fatalf("remove record: %v", err)
	}
	done = startTurn(t.Context(), r, "second, without an asker")
	awaitRecord(t, env["RECORD"])
	answer := askHook(t, path, "req-2")
	releaseTurn(t, env, 2)
	awaitTurn(t, done)
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-2" {
		t.Fatalf("answer on a turn without asker = %+v, want deny keeping req-2", answer)
	}
}

func TestApprovalAskerContextEndsWithTurn(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	captured := make(chan context.Context, 1)
	asker := func(ctx context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		captured <- ctx
		<-ctx.Done()
		return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionDeny}
	}
	ctx := contracts.WithApprovalAsker(t.Context(), asker)

	done := startTurn(ctx, r, "run the tool")
	path := awaitRecord(t, env["RECORD"])
	hook := make(chan error, 1)
	go func() {
		askCtx, cancel := context.WithTimeout(context.Background(), approvalTestWait)
		defer cancel()
		_, err := approvalsock.Ask(askCtx, path, contracts.ApprovalRequest{ID: "req-3", Tool: "Bash"})
		hook <- err
	}()
	awaitWithin(t, captured, "the asker")
	releaseTurn(t, env, 1)
	awaitTurn(t, done)
	if err := awaitWithin(t, hook, "the hook answer"); err != nil {
		t.Fatalf("hook: %v", err)
	}
}

func TestApprovalNoAskerLaunchesWithoutSocketVariable(t *testing.T) {
	t.Setenv(approvalsock.EnvApprovalSocket, "/inherited/from/daemon.sock")
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)

	done := startTurn(t.Context(), r, "run without an asker")
	if got := awaitRecord(t, env["RECORD"]); got != "unset" {
		t.Fatalf("CLI saw %s=%q, want no variable", approvalsock.EnvApprovalSocket, got)
	}
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("turn: %v", outcome.err)
	}
	if r.approvals != nil {
		t.Fatal("a turn without asker opened an approval relay")
	}
}

func TestApprovalTurnRefusedWhenSessionLaunchedWithoutSocket(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	releaseTurn(t, env, 1)
	first := startTurn(t.Context(), r, "first, without an asker")
	awaitRecord(t, env["RECORD"])
	if outcome := awaitTurn(t, first); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}

	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	outcome := awaitTurn(t, startTurn(ctx, r, "second, with an asker"))
	if !errors.Is(outcome.err, ErrApprovalUnreachable) {
		t.Fatalf("second turn err = %v, want ErrApprovalUnreachable", outcome.err)
	}
	if r.approvals != nil {
		t.Fatal("a refused turn opened an approval relay on a bare session")
	}
}

func TestApprovalRelayKeepsSocketAcrossRelaunch(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "first")
	firstPath := awaitRecord(t, env["RECORD"])
	releaseTurn(t, env, 1)
	awaitTurn(t, done)

	sess := r.currentSession()
	if sess == nil {
		t.Fatal("no live session after the first turn")
	}
	if err := r.dropSession(sess); err != nil {
		t.Fatalf("drop session: %v", err)
	}
	if err := os.Remove(env["RECORD"]); err != nil {
		t.Fatalf("remove record: %v", err)
	}

	done = startTurn(ctx, r, "second, after a relaunch")
	secondPath := awaitRecord(t, env["RECORD"])
	releaseTurn(t, env, 2)
	awaitTurn(t, done)
	if secondPath != firstPath {
		t.Fatalf("relaunched CLI got socket %q, want the same %q", secondPath, firstPath)
	}
}

func TestApprovalDirectoryIsPrivate(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "run the tool")
	path := awaitRecord(t, env["RECORD"])
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat approval directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("approval directory mode = %o, want 700", perm)
	}
	releaseTurn(t, env, 1)
	awaitTurn(t, done)
}

func TestApprovalCloseRemovesSocketDirectory(t *testing.T) {
	env := approvalEnv(t)
	r := newStreamResponder(t.Context(), []string{"sh", "-c", approvalTurnScript}, "", "", t.TempDir(), env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	done := startTurn(ctx, r, "run the tool")
	path := awaitRecord(t, env["RECORD"])
	releaseTurn(t, env, 1)
	awaitTurn(t, done)

	if err := returnWithin(t, "Close", r.Close); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("approval directory after Close: err = %v, want it removed", err)
	}
}
