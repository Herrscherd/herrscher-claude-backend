package claude

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	resumeLaterTurnsCLI = `log=$1; shift; echo "$*" >> "$log"; id=sid-new; case "$*" in *--resume*) id=sid-old;; esac; served=0; while read line; do case "$line" in *planted*) printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$id\"}"; exit 1;; *later*) if [ "$served" = 1 ]; then printf '%s\n' "{\"type\":\"result\",\"is_error\":true,\"subtype\":\"error_during_execution\",\"session_id\":\"$id\",\"errors\":[\"refused\"]}"; exit 1; fi;; *refuse*) printf '%s\n' "{\"type\":\"result\",\"is_error\":true,\"subtype\":\"error_during_execution\",\"session_id\":\"$id\",\"errors\":[\"refused\"]}"; exit 1;; esac; printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$id\"}" "{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"$id\"}"; served=1; done`

	answerThenDieCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; case "$n" in 1) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}'; exit 1;; 2) exit 1;; 3) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"two","session_id":"sid-old"}'; read line; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"three","session_id":"sid-old"}'; while read line; do printf '%s\n' '{"type":"result","is_error":false,"result":"three","session_id":"sid-old"}'; done`

	midCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; case "$n" in 1) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}'; exit 1;; 2) exit 1;; 3) printf '%s\n' '{"type":"result","is_error":false,"result":"two","session_id":"sid-b"}'; read line; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-b"}' '{"type":"result","is_error":false,"result":"three","session_id":"sid-b"}'; while read line; do printf '%s\n' '{"type":"result","is_error":false,"result":"three","session_id":"sid-b"}'; done`
)

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

func TestSilentDeathsCountPerDeathAcrossAnsweredTurns(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, answerThenDieCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the planted turn: the CLI dies after init, want an error")
	}
	var secondKinds []string
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "two"}, func(event contracts.BackendEvent) {
		secondKinds = append(secondKinds, event.Kind)
	})
	if err != nil {
		t.Fatalf("the second turn: %v: one silent death on an accepted resume is retried with the same id", err)
	}
	if reply != "two" {
		t.Fatalf("second reply = %q, want %q", reply, "two")
	}
	if slices.Contains(secondKinds, "reset") {
		t.Fatalf("second turn events = %q, want no reset: the resume is kept", secondKinds)
	}
	var thirdKinds []string
	reply, err = respondWithin(context.Background(), t, r, contracts.Prompt{Content: "three"}, func(event contracts.BackendEvent) {
		thirdKinds = append(thirdKinds, event.Kind)
	})
	if err != nil {
		t.Fatalf("the third turn: %v: a silent death after an answered turn is one death, the retry keeps the resume", err)
	}
	if reply != "three" {
		t.Fatalf("third reply = %q, want %q", reply, "three")
	}
	if slices.Contains(thirdKinds, "reset") {
		t.Fatalf("third turn events = %q, want no reset: an answered turn restarts the count of silent deaths", thirdKinds)
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 4 {
		t.Fatalf("the CLI was spawned %d times, want 4: the planted launch, the retry of turn two, the answering launch, the retry of turn three", len(argvs))
	}
	for i, argv := range argvs {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: deaths separated by an answered turn each keep the id", i+1, argv)
		}
	}
	mustClose(t, r)
}

func TestAnsweredTurnOnANewIDRestartsTheDeathCount(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, midCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the planted turn: the CLI dies after init, want an error")
	}
	if reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "two"}, nil); err != nil || reply != "two" {
		t.Fatalf("the second turn: reply %q, err %v: one silent death, then a retry that answers with a new id", reply, err)
	}
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "three"}, nil)
	if err != nil {
		t.Fatalf("the third turn: %v: the new id had its own count, one silent death keeps it", err)
	}
	if reply != "three" {
		t.Fatalf("third reply = %q, want %q", reply, "three")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 4 {
		t.Fatalf("the CLI was spawned %d times, want 4", len(argvs))
	}
	if !strings.Contains(argvs[3], "--resume sid-b") {
		t.Fatalf("the launch after the new id's silent death args = %q, want --resume sid-b: a change of id restarts the count", argvs[3])
	}
	mustClose(t, r)
}
