package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	identityScenarioCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); mode=fresh; case "$*" in *"--resume sid-a"*) mode=a;; *"--resume sid-b"*) mode=b;; esac; while read line; do case "$mode" in a) case "$line" in *die*) exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-a"}' '{"type":"result","is_error":false,"result":"ok","session_id":"sid-a"}';; b) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-b","errors":["refused"]}'; exit 1;; fresh) case "$line" in *stop*) if [ "$n" = 3 ]; then exit 1; fi;; esac; id=sid-c; if [ "$n" = 3 ]; then id=sid-b; fi; printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$id\"}" "{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"$id\"}";; esac; done`

	freshDiesOnceCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); while read line; do if [ "$n" = 1 ]; then exit 1; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"ok","session_id":"sid-new"}'; done`
)

func TestAcceptedFlagDoesNotTurnANewResumeIDRefusalIntoATurnError(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, identityScenarioCLI), "", "sid-a", "", nil)

	if reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "ok"}, nil); err != nil || reply != "ok" {
		t.Fatalf("the first turn: reply %q, err %v: the resume on sid-a is accepted", reply, err)
	}
	var secondKinds []string
	if reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "die"}, func(event contracts.BackendEvent) {
		secondKinds = append(secondKinds, event.Kind)
	}); err != nil || reply != "ok" {
		t.Fatalf("the second turn: reply %q, err %v: two silent deaths on sid-a fall back to a fresh session that answers with sid-b", reply, err)
	}
	if !strings.Contains(strings.Join(secondKinds, ","), "reset") {
		t.Fatalf("second turn events = %q, want a reset: the resume on sid-a is lost", secondKinds)
	}
	if reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "stop"}, nil); err != nil || reply != "ok" {
		t.Fatalf("the third turn: reply %q, err %v: a resume on sid-b refused before any line is replied by a fresh session, not a turn error", reply, err)
	}

	assertSpawnResumes(t, spawnedArgvs(t, log), []string{"--resume sid-a", "--resume sid-a", "", "--resume sid-b", ""})
	if got := r.ResumeToken(); got != "sid-c" {
		t.Fatalf("ResumeToken() = %q, want %q: the refused sid-b is not kept", got, "sid-c")
	}
	mustClose(t, r)
}

func assertSpawnResumes(t *testing.T, argvs, wantResume []string) {
	t.Helper()
	if len(argvs) != len(wantResume) {
		t.Fatalf("the CLI was spawned %d times, want %d", len(argvs), len(wantResume))
	}
	for i, want := range wantResume {
		if want == "" && strings.Contains(argvs[i], "--resume") {
			t.Fatalf("spawn %d args = %q, want no --resume", i+1, argvs[i])
		}
		if want != "" && !strings.Contains(argvs[i], want) {
			t.Fatalf("spawn %d args = %q, want %q", i+1, argvs[i], want)
		}
	}
}

func TestSilentDeathOfAFreshSessionOnItsFirstTurnIsRelaunchedOnce(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, freshDiesOnceCLI), "", "", "", nil)

	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "hello"}, nil)
	if err != nil {
		t.Fatalf("the turn: %v: no line was written, so the one relaunch of a fresh session answers the turn", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want %q", reply, "ok")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 2 {
		t.Fatalf("the CLI was spawned %d times, want 2: the silent death, then the relaunch", len(argvs))
	}
	for i, argv := range argvs {
		if strings.Contains(argv, "--resume") {
			t.Fatalf("spawn %d args = %q, want no --resume: the session was never resumed", i+1, argv)
		}
	}
	mustClose(t, r)
}
