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
	acceptedThenDyingCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -gt 1 ]; then exit 1; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}'; exit 1`

	realRefusalCLI = `log=$1; shift; echo "$*" >> "$log"; read line; case "$*" in *--resume*) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-old","errors":["No conversation found with session ID: sid-old"]}'; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'`

	errorAfterInitCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -gt 1 ]; then printf '%s\n' '{"type":"result","is_error":false,"result":"again","session_id":"sid-old"}'; exit 0; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":true,"result":"boom","session_id":"sid-old"}'; exit 0`

	acceptedThenRefusedCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -eq 1 ]; then printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}'; exit 1; fi; case "$*" in *sid-old*) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-old","errors":["No conversation found with session ID: sid-old"]}'; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'; while read line; do printf '%s\n' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'; done`

	refuseAllCLI = `log=$1; shift; echo "$*" >> "$log"; read line; printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","errors":["refused"]}'; exit 1`
)

func TestAcceptedResumeKeepsItsIDOnOneSilentDeath(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, acceptedThenDyingCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "one"}, nil); err == nil {
		t.Fatal("the first turn: the CLI dies after init, want an error")
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "two"}, nil); err == nil {
		t.Fatal("the second turn: the CLI dies silently, want an error")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 3 {
		t.Fatalf("the CLI was spawned %d times, want 3: the accepted resume, one silent death, its retry in the same turn", len(argvs))
	}
	for i, argv := range argvs {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: one silent death on an accepted resume keeps its id", i+1, argv)
		}
	}
	mustClose(t, r)
}

func TestAcceptedResumeSurvivesAPlantedTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, acceptedThenDyingCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the planted turn: the CLI dies after init, want an error")
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "next"}, nil); err == nil {
		t.Fatal("the next turn: the CLI dies before any line, want an error")
	}
	for i, argv := range spawnedArgvs(t, log) {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: a turn that errored after init still took the resume", i+1, argv)
		}
	}
	mustClose(t, r)
}

func TestRealRefusalOfAResumeIsRepliedFreshInTheSameTurn(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, realRefusalCLI), "", "sid-old", "", nil)

	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil)
	if err != nil {
		t.Fatalf("the first turn after a real refusal: %v: the CLI refused --resume with a result line, the fresh session must answer this turn", err)
	}
	if reply != "fresh" {
		t.Fatalf("reply = %q, want %q", reply, "fresh")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 2 {
		t.Fatalf("the CLI was spawned %d times, want 2: the refused resume, then one fresh start", len(argvs))
	}
	if strings.Contains(argvs[1], "--resume") {
		t.Fatalf("fresh spawn args = %q, want no --resume: the refused id must not be sent again", argvs[1])
	}
	if got := r.ResumeToken(); got != "sid-new" {
		t.Fatalf("ResumeToken() = %q, want %q", got, "sid-new")
	}
	mustClose(t, r)
}

func TestAcceptedThenRefusedResumeFallsBackFresh(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, acceptedThenRefusedCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "planted"}, nil); err == nil {
		t.Fatal("the first turn: the CLI dies after init, want an error")
	}
	var kinds []string
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "refused"}, func(event contracts.BackendEvent) {
		kinds = append(kinds, event.Kind)
	})
	if err != nil {
		t.Fatalf("the second turn: %v: the resume was accepted on the first turn, then refused by the CLI: the turn must be answered by a fresh session", err)
	}
	if reply != "fresh" {
		t.Fatalf("reply = %q, want %q", reply, "fresh")
	}
	if !slices.Contains(kinds, "reset") {
		t.Fatalf("events = %q, want a reset announcing the lost context", kinds)
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 3 {
		t.Fatalf("the CLI was spawned %d times, want 3: the accepted resume, its refusal, then one fresh start", len(argvs))
	}
	if !strings.Contains(argvs[1], "--resume sid-old") {
		t.Fatalf("second spawn args = %q, want --resume sid-old", argvs[1])
	}
	if strings.Contains(argvs[2], "--resume") {
		t.Fatalf("fresh spawn args = %q, want no --resume: the refused id must not be sent again", argvs[2])
	}
	reply, err = respondWithin(context.Background(), t, r, contracts.Prompt{Content: "third"}, nil)
	if err != nil {
		t.Fatalf("the third turn: %v: the fresh session must keep answering", err)
	}
	if reply != "fresh" {
		t.Fatalf("third reply = %q, want %q", reply, "fresh")
	}
	if n := len(spawnedArgvs(t, log)); n != 3 {
		t.Fatalf("the CLI was spawned %d times after the third turn, want 3: no further resume is attempted", n)
	}
	mustClose(t, r)
}

func TestRefusedFallbackThatIsRefusedAgainFailsOnce(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, refuseAllCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err == nil {
		t.Fatal("the first turn: the CLI refuses the resume and the fallback, want an error")
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 2 {
		t.Fatalf("the CLI was spawned %d times, want 2: one refused resume, then one fallback, never a loop", len(argvs))
	}
	if !strings.Contains(argvs[0], "--resume sid-old") {
		t.Fatalf("first spawn args = %q, want --resume sid-old", argvs[0])
	}
	if strings.Contains(argvs[1], "--resume") {
		t.Fatalf("fallback spawn args = %q, want no --resume", argvs[1])
	}
	if got := r.ResumeToken(); got != "" {
		t.Fatalf("ResumeToken() = %q, want empty: the CLI announced no session", got)
	}
	mustClose(t, r)
}

func TestErrorResultAfterInitKeepsTheResume(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, errorAfterInitCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err == nil {
		t.Fatal("the first turn: the CLI answers an error result after init, want an error")
	}
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, nil)
	if err != nil {
		t.Fatalf("the second turn: %v", err)
	}
	if reply != "again" {
		t.Fatalf("reply = %q, want %q", reply, "again")
	}
	for i, argv := range spawnedArgvs(t, log) {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: an error result after init is a real turn error, not a lost resume", i+1, argv)
		}
	}
	mustClose(t, r)
}
