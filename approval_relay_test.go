package claude

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/approvalsock"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const oneShotApprovalScript = `printf '%s' "${HERRSCHER_APPROVAL_SOCKET-unset}" > "$RECORD"
while [ ! -f "$GATE" ]; do sleep 0.01; done
printf 'ok'
`

func openTestRelay(tb testing.TB) *approvalRelay {
	tb.Helper()
	relay, err := openApprovalRelay(tb.Context())
	if err != nil {
		tb.Fatalf("openApprovalRelay: %v", err)
	}
	tb.Cleanup(func() {
		if err := returnWithin(tb, "close", relay.close); err != nil {
			tb.Errorf("close relay: %v", err)
		}
	})
	return relay
}

func TestRequirePrivateDirAcceptsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateDir(dir); err != nil {
		t.Fatalf("requirePrivateDir(0700) = %v, want nil", err)
	}
}

func TestRequirePrivateDirRejectsGroupReadable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := requirePrivateDir(dir)
	if !errors.Is(err, approvalsock.ErrInsecureDirectory) {
		t.Fatalf("requirePrivateDir(0755) = %v, want ErrInsecureDirectory", err)
	}
}

func TestOpenApprovalRelayBindsPrivateSocket(t *testing.T) {
	relay := openTestRelay(t)
	info, err := os.Stat(filepath.Dir(relay.path))
	if err != nil {
		t.Fatalf("stat relay directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("relay directory mode = %o, want 700", perm)
	}
	if _, err := os.Stat(relay.path); err != nil {
		t.Fatalf("socket not bound: %v", err)
	}
}

func TestRelayDeniesWhenNoTurnWaits(t *testing.T) {
	relay := openTestRelay(t)
	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-4", Tool: "Bash"})
	want := newMessages().english(keyApprovalNoTurn)
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-4" || answer.Reason != want {
		t.Fatalf("answer = %+v, want deny for req-4 with %q", answer, want)
	}
}

func TestRelayDeniesMismatchedAnswerID(t *testing.T) {
	relay := openTestRelay(t)
	asker := func(_ context.Context, _ contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: "someone-else", Decision: contracts.DecisionAllow}
	}
	endTurn := relay.beginTurn(t.Context(), asker, i18n.English)
	defer endTurn()

	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-5", Tool: "Bash"})
	want := newMessages().english(keyApprovalMismatch)
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-5" || answer.Reason != want {
		t.Fatalf("answer = %+v, want deny for req-5 with %q", answer, want)
	}
}

func TestRelayForwardsAnswerOfActiveTurn(t *testing.T) {
	relay := openTestRelay(t)
	asker := func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow, Reason: "approved"}
	}
	endTurn := relay.beginTurn(t.Context(), asker, i18n.English)
	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-6", Tool: "Bash"})
	endTurn()

	if !answer.Allowed() || answer.ID != "req-6" || answer.Reason != "approved" {
		t.Fatalf("answer = %+v, want the asker's allow for req-6", answer)
	}
}

func TestRelayClearsTurnAfterEnd(t *testing.T) {
	relay := openTestRelay(t)
	asker := func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow}
	}
	endTurn := relay.beginTurn(t.Context(), asker, i18n.English)
	endTurn()

	if turn, _ := relay.current(); turn != nil {
		t.Fatal("turn still set after its end")
	}
	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-7"})
	if answer.Decision != contracts.DecisionDeny {
		t.Fatalf("answer after the turn ended = %+v, want deny", answer)
	}
}

func TestRelayEnvCopiesBaseWithoutMutation(t *testing.T) {
	relay := openTestRelay(t)
	base := map[string]string{"KEEP": "1"}
	env := relay.env(base)
	if env[approvalsock.EnvApprovalSocket] != relay.path || env["KEEP"] != "1" {
		t.Fatalf("env = %v, want the socket path and KEEP", env)
	}
	if _, touched := base[approvalsock.EnvApprovalSocket]; touched {
		t.Fatal("env mutated the base map")
	}
}

func TestApprovalSocketDirChoosesBase(t *testing.T) {
	t.Parallel()
	fits := "/" + strings.Repeat("d", 62)
	tooLong := "/" + strings.Repeat("d", 63)
	cases := []struct {
		name     string
		base     string
		fallback string
		limit    int
		want     string
		wantErr  bool
	}{
		{name: "base fits the limit", base: fits, fallback: "/tmp", limit: 108, want: fits},
		{name: "base at the limit falls back", base: tooLong, fallback: "/tmp", limit: 108, want: "/tmp"},
		{name: "104 limit falls back earlier", base: fits, fallback: "/tmp", limit: 104, want: "/tmp"},
		{name: "no fallback on this platform", base: tooLong, fallback: "", limit: 108, wantErr: true},
		{name: "fallback too long as well", base: tooLong, fallback: tooLong, limit: 108, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := approvalSocketDir(tc.base, tc.fallback, tc.limit)
			if tc.wantErr {
				if !errors.Is(err, ErrApprovalSocketPathTooLong) {
					t.Fatalf("approvalSocketDir err = %v, want ErrApprovalSocketPathTooLong", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("approvalSocketDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestApprovalSocketPathTooLongIsLocalized(t *testing.T) {
	t.Parallel()
	_, err := approvalSocketDir("/"+strings.Repeat("d", 120), "", 108)
	if !errors.Is(err, ErrApprovalSocketPathTooLong) {
		t.Fatalf("approvalSocketDir err = %v, want ErrApprovalSocketPathTooLong", err)
	}
	if want := newMessages().localized(i18n.French, keyApprovalPathTooLong); want == newMessages().english(keyApprovalPathTooLong) {
		t.Fatal("the French path-too-long message is missing from the catalog")
	}
	for _, lang := range []i18n.Language{i18n.English, i18n.French} {
		got := localizeTurnEnd(lang, fmt.Errorf("%w: %w", errApprovalOpen, err))
		want := newMessages().localized(lang, keyApprovalPathTooLong)
		if got.Error() != want {
			t.Fatalf("%s turn err = %q, want %q", lang, got.Error(), want)
		}
		if !errors.Is(got, ErrApprovalSocketPathTooLong) {
			t.Fatalf("%s turn err %v lost ErrApprovalSocketPathTooLong", lang, got)
		}
		if !strings.Contains(want, "TMPDIR") {
			t.Fatalf("%s path-too-long message %q does not name TMPDIR", lang, want)
		}
	}
}

func TestRelayOpensUnderFallbackWhenTMPDIRIsLong(t *testing.T) {
	if approvalFallbackDir == "" {
		t.Skip("this platform has no fallback directory")
	}
	t.Setenv("TMPDIR", longTempRoot(t))
	relay := openTestRelay(t)
	if !strings.HasPrefix(relay.path, approvalFallbackDir+string(filepath.Separator)) {
		t.Fatalf("socket %q is not under the fallback %s", relay.path, approvalFallbackDir)
	}
	endTurn := relay.beginTurn(t.Context(), allowingAsker(), i18n.English)
	defer endTurn()

	answer := askHook(t, relay.path, "req-long-tmpdir")
	if !answer.Allowed() || answer.ID != "req-long-tmpdir" {
		t.Fatalf("answer = %+v, want the asker's allow through the fallback socket", answer)
	}
}

func TestRelayCloseRemovesDirectory(t *testing.T) {
	relay, err := openApprovalRelay(t.Context())
	if err != nil {
		t.Fatalf("openApprovalRelay: %v", err)
	}
	dir := filepath.Dir(relay.path)
	if err := returnWithin(t, "close", relay.close); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("relay directory after close: err = %v, want it removed", err)
	}
}

func oneShotScript(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "fake-cli.sh")
	if err := os.WriteFile(path, []byte(oneShotApprovalScript), 0o600); err != nil {
		tb.Fatal(err)
	}
	return "sh " + path
}

func oneShotEnv(tb testing.TB) map[string]string {
	tb.Helper()
	dir := tb.TempDir()
	return map[string]string{"RECORD": filepath.Join(dir, "record"), "GATE": filepath.Join(dir, "gate")}
}

func runOneShotAsync(ctx context.Context, cmd string, env map[string]string) <-chan turnOutcome {
	done := make(chan turnOutcome, 1)
	go func() {
		out, err := runOneShot(ctx, cmd, env, contracts.Prompt{Content: "run the tool"})
		done <- turnOutcome{text: out, err: err}
	}()
	return done
}

func TestOneShotWithAskerExposesSocketAndForwardsAnswer(t *testing.T) {
	env := oneShotEnv(t)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	done := runOneShotAsync(ctx, oneShotScript(t), env)

	path := awaitRecord(t, env["RECORD"])
	answer := askHook(t, path, "one-1")
	if !answer.Allowed() || answer.ID != "one-1" {
		t.Fatalf("hook answer = %+v, want allow for one-1", answer)
	}
	if err := os.WriteFile(env["GATE"], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := awaitTurn(t, done)
	if outcome.err != nil {
		t.Fatalf("runOneShot: %v", outcome.err)
	}
	if outcome.text != "ok" {
		t.Fatalf("runOneShot output = %q, want ok", outcome.text)
	}
}

func TestOneShotRemovesSocketDirectoryAfterRun(t *testing.T) {
	env := oneShotEnv(t)
	ctx := contracts.WithApprovalAsker(t.Context(), allowingAsker())
	done := runOneShotAsync(ctx, oneShotScript(t), env)

	path := awaitRecord(t, env["RECORD"])
	if err := os.WriteFile(env["GATE"], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if outcome := awaitTurn(t, done); outcome.err != nil {
		t.Fatalf("runOneShot: %v", outcome.err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("approval directory after the run: err = %v, want it removed", err)
	}
}

func TestOneShotWithoutAskerDropsInheritedSocket(t *testing.T) {
	t.Setenv(approvalsock.EnvApprovalSocket, "/inherited/from/daemon.sock")
	env := oneShotEnv(t)
	if err := os.WriteFile(env["GATE"], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := awaitTurn(t, runOneShotAsync(t.Context(), oneShotScript(t), env))
	if outcome.err != nil {
		t.Fatalf("runOneShot: %v", outcome.err)
	}
	if got := awaitRecord(t, env["RECORD"]); got != "unset" {
		t.Fatalf("CLI saw %s=%q, want no variable", approvalsock.EnvApprovalSocket, got)
	}
}

func TestSpawnEnvDropsInheritedSocketWithoutRelay(t *testing.T) {
	base := []string{approvalsock.EnvApprovalSocket + "=/inherited.sock", "PATH=/usr/bin"}
	for _, entry := range spawnEnv(base, nil) {
		if entry == approvalsock.EnvApprovalSocket+"=/inherited.sock" {
			t.Fatal("inherited approval socket survived spawnEnv")
		}
	}
}

func TestApprovalRelayOpensOnFirstRequest(t *testing.T) {
	r := newStreamResponder(t.Context(), []string{"sh", "-c", "true"}, "", "", t.TempDir(), nil)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	relay, err := r.approvalRelayFor(t.Context())
	if err != nil {
		t.Fatalf("approvalRelayFor on a fresh responder: %v", err)
	}
	if relay == nil || r.approvals != relay {
		t.Fatal("first approval request did not store its relay on the responder")
	}
}

func TestRelayDeniesMismatchInTurnLanguage(t *testing.T) {
	relay := openTestRelay(t)
	asker := func(_ context.Context, _ contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: "someone-else", Decision: contracts.DecisionAllow}
	}
	endTurn := relay.beginTurn(t.Context(), asker, i18n.French)
	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-fr", Tool: "Bash"})
	endTurn()

	want := newMessages().localized(i18n.French, keyApprovalMismatch)
	if want == newMessages().english(keyApprovalMismatch) {
		t.Fatal("the French mismatch reason is the English one")
	}
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-fr" || answer.Reason != want {
		t.Fatalf("answer = %+v, want deny for req-fr with %q", answer, want)
	}
}

func TestRelayKeepsTurnLanguageOutsideTurn(t *testing.T) {
	relay := openTestRelay(t)
	asker := func(_ context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		return contracts.ApprovalAnswer{ID: req.ID, Decision: contracts.DecisionAllow}
	}
	relay.beginTurn(t.Context(), asker, i18n.French)()

	answer := relay.answer(t.Context(), contracts.ApprovalRequest{ID: "req-fr-2", Tool: "Bash"})
	want := newMessages().localized(i18n.French, keyApprovalNoTurn)
	if answer.Decision != contracts.DecisionDeny || answer.ID != "req-fr-2" || answer.Reason != want {
		t.Fatalf("answer after a French turn = %+v, want deny for req-fr-2 with %q", answer, want)
	}
}
