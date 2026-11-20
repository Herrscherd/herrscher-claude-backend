package claude

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const acceptingFreshCLI = `log=$1; shift; echo "$*" >> "$log"; read line; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`

func TestDiscardedResumeIDEmitsNoticeWithoutResume(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, acceptingFreshCLI), "", "--foo", "", nil)

	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "run"}, recordEvents(&events))
	if err != nil || reply != "done" {
		t.Fatalf("reply %q, err %v: an unusable resume id must not stop the turn", reply, err)
	}
	requireLifecycleKinds(t, events, []string{contracts.BackendKindNotice}, "a discarded resume id gives one resume-lost notice and no reset")
	assertSpawnResumes(t, spawnedArgvs(t, log), []string{""})
	mustClose(t, r)
}

func TestRelayOpenFailureIsReportedInTurnLanguage(t *testing.T) {
	env := approvalEnv(t)
	dir := absentTempRoot(t)
	t.Setenv("TMPDIR", dir)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	_, err := respondWithin(ctx, t, r, contracts.Prompt{Content: "run the tool", Language: i18n.French}, nil)
	if !errors.Is(err, errApprovalOpen) {
		t.Fatalf("turn err = %v, want the open failure to keep errApprovalOpen", err)
	}
	want := dirFailedMessage(i18n.French, dir)
	if want == dirFailedMessage(i18n.English, dir) {
		t.Fatal("the French directory-failure message is missing from the catalog")
	}
	if err.Error() != want {
		t.Fatalf("turn err text = %q, want the French message %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("French directory-failure message %q does not name the directory tried %q", err.Error(), dir)
	}
}

func TestAskerlessLaunchReplacesStoppedRelay(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(ctx, r, "first, with an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	old := r.approvals
	if err := returnWithin(t, "close the relay", old.close); err != nil {
		t.Fatalf("close relay: %v", err)
	}
	if err := returnWithin(t, "drop the bound session", func() error { return r.dropSession(r.currentSession()) }); err != nil {
		t.Fatalf("drop bound session: %v", err)
	}
	removeFiles(t, env["RECORD"], env["ARGS"], env["GATE"]+"1")
	releaseTurn(t, env, 1)

	outcome := awaitTurn(t, startTurn(t.Context(), r, "second, without an asker, after the relay stopped"))
	if outcome.err != nil {
		t.Fatalf("asker-less turn after the relay stopped: %v", outcome.err)
	}
	fresh := r.approvals
	if fresh == nil || fresh == old || !fresh.serving() {
		t.Fatal("asker-less launch did not install a served relay in place of the stopped one")
	}
	if path := awaitRecord(t, env["RECORD"]); path != fresh.path {
		t.Fatalf("CLI launched with socket %q, want the new relay %q", path, fresh.path)
	}
	if args := awaitRecord(t, env["ARGS"]); !strings.Contains(args, "--resume s1") {
		t.Fatalf("relaunched CLI args = %q, want --resume s1", args)
	}
	if !sessionBound(r) {
		t.Fatal("session launched after the relay stopped is not bound to the new relay")
	}
}

func TestOneShotOpenFailureIsReportedInRequestLanguage(t *testing.T) {
	dir := absentTempRoot(t)
	t.Setenv("TMPDIR", dir)
	b := mustBackend(t, Config{Kind: "oneshot", Cmd: "printf %s"})
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())

	_, err := respondWithin(ctx, t, b, contracts.Prompt{Content: "run the tool", Language: i18n.French}, nil)
	if !errors.Is(err, errApprovalOpen) {
		t.Fatalf("oneshot err = %v, want the open failure to keep errApprovalOpen", err)
	}
	want := dirFailedMessage(i18n.French, dir)
	if err.Error() != want {
		t.Fatalf("oneshot err text = %q, want the French message %q", err.Error(), want)
	}
}

func TestAskerlessLaunchOnBoundSessionFailsWhenRelayCannotReopen(t *testing.T) {
	env := approvalEnv(t)
	r := newApprovalResponder(t, env)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	releaseTurn(t, env, 1)
	if outcome := awaitTurn(t, startTurn(ctx, r, "first, with an asker")); outcome.err != nil {
		t.Fatalf("first turn: %v", outcome.err)
	}
	old := r.approvals
	if old == nil {
		t.Fatal("first turn did not bind the session to a relay")
	}
	if err := returnWithin(t, "close the relay", old.close); err != nil {
		t.Fatalf("close relay: %v", err)
	}
	if err := returnWithin(t, "drop the bound session", func() error { return r.dropSession(r.currentSession()) }); err != nil {
		t.Fatalf("drop bound session: %v", err)
	}
	removeFiles(t, env["RECORD"], env["ARGS"])
	dir := absentTempRoot(t)
	t.Setenv("TMPDIR", dir)

	_, err := respondWithin(t.Context(), t, r, contracts.Prompt{Content: "second, without an asker", Language: i18n.French}, nil)
	want := dirFailedMessage(i18n.French, dir)
	if err == nil || err.Error() != want {
		t.Fatalf("asker-less turn err = %v, want the French message %q", err, want)
	}
	if _, statErr := os.Stat(env["ARGS"]); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the CLI was launched although the relay could not reopen (args file stat: %v)", statErr)
	}
}
