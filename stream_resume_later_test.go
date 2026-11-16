package claude

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const resumeLaterTurnsCLI = `log=$1; shift; echo "$*" >> "$log"; id=sid-new; case "$*" in *--resume*) id=sid-old;; esac; served=0; while read line; do case "$line" in *planted*) printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$id\"}"; exit 1;; *later*) if [ "$served" = 1 ]; then printf '%s\n' "{\"type\":\"result\",\"is_error\":true,\"subtype\":\"error_during_execution\",\"session_id\":\"$id\",\"errors\":[\"refused\"]}"; exit 1; fi;; *refuse*) printf '%s\n' "{\"type\":\"result\",\"is_error\":true,\"subtype\":\"error_during_execution\",\"session_id\":\"$id\",\"errors\":[\"refused\"]}"; exit 1;; *boom*) case "$*" in *--resume*) exit 1;; esac;; esac; printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$id\"}" "{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"$id\"}"; served=1; done`

func TestResultErrorOnALaterTurnOfAResumedSessionIsATurnError(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeLaterTurnsCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "ok"}, nil); err != nil {
		t.Fatalf("the first turn: %v: the resume is accepted on this turn", err)
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "later"}, nil); err == nil {
		t.Fatal("the second turn: a result error alone, on a session that already took a turn, is a turn error: want an error")
	}
	for i, argv := range spawnedArgvs(t, log) {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: a result error on a later turn is not a refused resume, no fresh launch is allowed", i+1, argv)
		}
	}
	if got := r.ResumeToken(); got != "sid-old" {
		t.Fatalf("ResumeToken() = %q, want %q", got, "sid-old")
	}
	mustClose(t, r)
}

func TestSilentDeathsOnAnAcceptedResumeFallBackFreshOnTheSecondTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeLaterTurnsCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the planted turn: the CLI dies after init, want an error")
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "boom"}, nil); err == nil {
		t.Fatal("the first silent death of an accepted resume: want an error, the id is kept for one more try")
	}
	var secondKinds []string
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "boom"}, func(event contracts.BackendEvent) {
		secondKinds = append(secondKinds, event.Kind)
	})
	if err != nil {
		t.Fatalf("the second silent death: %v: the turn must be answered by a fresh session in the same turn", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want %q", reply, "ok")
	}
	if !slices.Contains(secondKinds, "reset") {
		t.Fatalf("second silent death events = %q, want a reset announcing the lost context", secondKinds)
	}
	argvs := spawnedArgvs(t, log)
	last := len(argvs) - 1
	if strings.Contains(argvs[last], "--resume") {
		t.Fatalf("fresh spawn args = %q, want no --resume", argvs[last])
	}
	for i, argv := range argvs[:last] {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old before the fallback", i+1, argv)
		}
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "boom"}, nil); err != nil {
		t.Fatalf("the third turn: %v: the fresh session must keep answering", err)
	}
	if n := len(spawnedArgvs(t, log)); n != len(argvs) {
		t.Fatalf("the CLI was spawned %d times after the third turn, want %d: the fresh session answers without another launch", n, len(argvs))
	}
	mustClose(t, r)
}

func TestSilentDeathThenAnswerThenSilentDeathKeepsTheResume(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeLaterTurnsCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the planted turn: the CLI dies after init, want an error")
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "boom"}, nil); err == nil {
		t.Fatal("the first silent death: want an error")
	}
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "ok"}, nil)
	if err != nil {
		t.Fatalf("the answered turn: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want %q", reply, "ok")
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "boom"}, nil); err == nil {
		t.Fatal("the second silent death: want an error, the answered turn reset the count so this is the first silent death again")
	}
	for i, argv := range spawnedArgvs(t, log) {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: a turn that answered resets the silent-death count", i+1, argv)
		}
	}
	if got := r.ResumeToken(); got != "sid-old" {
		t.Fatalf("ResumeToken() = %q, want %q", got, "sid-old")
	}
	mustClose(t, r)
}
