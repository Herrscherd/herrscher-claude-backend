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

const approvalTurnScript = `printf '%s' "$*" > "$ARGS"
n=0
while read -r _; do
	n=$((n+1))
	printf '%s' "${HERRSCHER_APPROVAL_SOCKET-unset}" > "$RECORD"
	while [ ! -f "$GATE$n" ]; do sleep 0.01; done
	printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}' '{"type":"result","is_error":false,"result":"ok","session_id":"'"$SESSION"'"}'
done`

type turnOutcome struct {
	text string
	err  error
}

func approvalEnv(tb testing.TB) map[string]string {
	tb.Helper()
	dir := tb.TempDir()
	return map[string]string{
		"RECORD":  filepath.Join(dir, "record"),
		"GATE":    filepath.Join(dir, "gate"),
		"ARGS":    filepath.Join(dir, "args"),
		"SESSION": "s1",
	}
}

func newApprovalResponder(tb testing.TB, env map[string]string) *streamResponder {
	tb.Helper()
	r := newStreamResponder(context.WithoutCancel(tb.Context()), []string{"sh", "-c", approvalTurnScript}, "", "", tb.TempDir(), env)
	tb.Cleanup(func() {
		if err := returnWithin(tb, "Close", r.Close); err != nil {
			tb.Errorf("Close: %v", err)
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

func releaseTurn(tb testing.TB, env map[string]string, turn int) {
	tb.Helper()
	if err := os.WriteFile(env["GATE"]+strconv.Itoa(turn), nil, 0o600); err != nil {
		tb.Fatalf("release turn %d: %v", turn, err)
	}
}

func awaitRecord(tb testing.TB, path string) string {
	tb.Helper()
	return returnWithin(tb, "the CLI to record its environment", func() string {
		for {
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				return string(data)
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func awaitTurn(tb testing.TB, done <-chan turnOutcome) turnOutcome {
	tb.Helper()
	return awaitWithin(tb, done, "the turn")
}

func askHook(tb testing.TB, path, id string) contracts.ApprovalAnswer {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), approvalTestWait)
	defer cancel()
	answer, err := approvalsock.Ask(ctx, path, contracts.ApprovalRequest{ID: id, Tool: "Bash", Subject: "ls"})
	if err != nil {
		tb.Fatalf("Ask %s: %v", id, err)
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
	type hookResult struct {
		answer contracts.ApprovalAnswer
		err    error
	}
	hook := make(chan hookResult, 1)
	go func() {
		askCtx, cancel := context.WithTimeout(context.Background(), approvalTestWait)
		defer cancel()
		answer, err := approvalsock.Ask(askCtx, path, contracts.ApprovalRequest{ID: "req-3", Tool: "Bash"})
		hook <- hookResult{answer: answer, err: err}
	}()
	awaitWithin(t, captured, "the asker")
	releaseTurn(t, env, 1)
	awaitTurn(t, done)
	res := awaitWithin(t, hook, "the hook answer")
	if res.err != nil {
		t.Fatalf("hook: %v", res.err)
	}
	if res.answer.Decision != contracts.DecisionDeny || res.answer.ID != "req-3" {
		t.Fatalf("hook answer after the turn ended = %+v, want deny keeping req-3", res.answer)
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
