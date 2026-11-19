package claude

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const keepaliveFreshCLI = `log=$1; shift; echo "$*" >> "$log"; while read line; do printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'; done`

const (
	resumeNeverAcceptedCLI       = `log=$1; shift; echo "$*" >> "$log"; case "$*" in *--resume*) read line; exit 1;; esac; read line; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`
	silentDeathsAfterAcceptedCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -eq 1 ]; then printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"first","session_id":"sid-old"}'; read line; exit 1; fi; if [ "$n" -eq 2 ]; then exit 1; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`
	refusedAfterAcceptedCLI      = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; case "$n" in 1) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"first","session_id":"sid-old"}'; read line; exit 1;; 2) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-old","errors":["No conversation found with session ID: sid-old"]}'; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'`
)

const refusedOnLaterTurnCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; case "$n" in 1) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}'; exit 1;; 2) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-old","errors":["No conversation found with session ID: sid-old"]}'; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'`

func recordEvents(events *[]contracts.BackendEvent) func(contracts.BackendEvent) {
	return func(event contracts.BackendEvent) {
		*events = append(*events, event)
	}
}

func lifecycleKinds(events []contracts.BackendEvent) []string {
	var kinds []string
	for i := range events {
		if events[i].Kind == contracts.BackendKindNotice || events[i].Kind == "reset" {
			kinds = append(kinds, events[i].Kind)
		}
	}
	return kinds
}

func requireLostResumeEvents(t *testing.T, events []contracts.BackendEvent) {
	t.Helper()
	requireLifecycleKinds(t, events, []string{contracts.BackendKindNotice, "reset"}, "a lost resume gives one notice, then the reset")
}

func requireLostAfterRelaunchEvents(t *testing.T, events []contracts.BackendEvent) {
	t.Helper()
	requireLifecycleKinds(t, events, []string{"reset", contracts.BackendKindNotice, "reset"}, "each relaunch gives a reset, and the lost resume adds one notice before its reset")
}

func requireLifecycleKinds(t *testing.T, events []contracts.BackendEvent, want []string, why string) {
	t.Helper()
	if !slices.Equal(lifecycleKinds(events), want) {
		t.Fatalf("lifecycle events = %q, want %q: %s", lifecycleKinds(events), want, why)
	}
	for i := range events {
		if events[i].Kind != contracts.BackendKindNotice {
			continue
		}
		notice := events[i].Notice
		if notice == nil || notice.Code != contracts.CodeResumeLost || len(notice.Args) != 0 {
			t.Fatalf("notice = %+v, want CodeResumeLost without arguments", notice)
		}
	}
}

func TestRefusalOnTheFirstLaunchOfALaterTurnEmitsTheNoticeBeforeTheReset(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, refusedOnLaterTurnCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err == nil {
		t.Fatal("the first turn: the CLI takes the turn and then fails it, want an error")
	}
	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, recordEvents(&events))
	if err != nil || reply != "fresh" {
		t.Fatalf("reply %q, err %v: a refusal on the first launch of a later turn must answer with a fresh session", reply, err)
	}
	requireLostResumeEvents(t, events)
	assertSpawnResumes(t, spawnedArgvs(t, log), []string{"--resume sid-old", "--resume sid-old", ""})
	mustClose(t, r)
}

func TestRefusalBeforeAnyAcceptedTurnEmitsTheResumeLostNoticeBeforeTheReset(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, realRefusalCLI), "", "sid-old", "", nil)

	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, recordEvents(&events))
	if err != nil || reply != "fresh" {
		t.Fatalf("reply %q, err %v: a refused resume must answer with a fresh session", reply, err)
	}
	requireLostResumeEvents(t, events)
	mustClose(t, r)
}

func TestRefusalAfterAnAcceptedTurnEmitsTheResumeLostNoticeBeforeTheReset(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, refusedAfterAcceptedCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err != nil {
		t.Fatalf("first turn, the resume is accepted: %v", err)
	}
	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, recordEvents(&events))
	if err != nil || reply != "fresh" {
		t.Fatalf("reply %q, err %v: a refusal of the id after an accepted turn must answer with a fresh session", reply, err)
	}
	requireLostAfterRelaunchEvents(t, events)
	assertSpawnResumes(t, spawnedArgvs(t, log), []string{"--resume sid-old", "--resume sid-old", ""})
	mustClose(t, r)
}

func TestResumeNeverAcceptedEmitsTheResumeLostNoticeBeforeTheReset(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeNeverAcceptedCLI), "", "sid-old", "", nil)

	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, recordEvents(&events))
	if err != nil || reply != "done" {
		t.Fatalf("reply %q, err %v: a resume that never took a line must fall back to a fresh session", reply, err)
	}
	requireLostResumeEvents(t, events)
	mustClose(t, r)
}

func TestTwoSilentDeathsAfterAnAcceptedTurnEmitTheResumeLostNoticeBeforeTheReset(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, silentDeathsAfterAcceptedCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err != nil {
		t.Fatalf("first turn, the resume is accepted: %v", err)
	}
	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, recordEvents(&events))
	if err != nil || reply != "done" {
		t.Fatalf("reply %q, err %v: two silent deaths on the accepted id must fall back to a fresh session", reply, err)
	}
	requireLostAfterRelaunchEvents(t, events)
	mustClose(t, r)
}

func TestFreshSessionDeathEmitsTheResetWithoutAResumeLostNotice(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, freshDiesOnceCLI), "", "", "", nil)

	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, recordEvents(&events))
	if err != nil || reply != "ok" {
		t.Fatalf("reply %q, err %v: a fresh session dying once is relaunched", reply, err)
	}
	if want := []string{"reset"}; !slices.Equal(lifecycleKinds(events), want) {
		t.Fatalf("lifecycle events = %q, want %q: no resume was lost, so no notice", lifecycleKinds(events), want)
	}
	mustClose(t, r)
}

func TestSilentDeathAfterAnAcceptedResumeEmitsOneResetAndNoNotice(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeLostAfterAcceptedTurnCLI), "", "sid-old", "", nil)

	var events []contracts.BackendEvent
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, recordEvents(&events)); err != nil {
		t.Fatalf("first turn, the resume is accepted: %v", err)
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, recordEvents(&events)); err != nil {
		t.Fatalf("second turn, one silent death of an accepted resume is relaunched: %v", err)
	}
	if want := []string{"reset"}; !slices.Equal(lifecycleKinds(events), want) {
		t.Fatalf("lifecycle events = %q, want %q: an accepted resume that dies once is relaunched with a reset and no notice", lifecycleKinds(events), want)
	}
	mustClose(t, r)
}

func TestDiscardedResumeNoticeWaitsForATurnWithAListener(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, keepaliveFreshCLI), "", "--foo", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first, no listener"}, nil); err != nil {
		t.Fatalf("first turn without a listener: %v", err)
	}
	var events []contracts.BackendEvent
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second, with a listener"}, recordEvents(&events)); err != nil {
		t.Fatalf("second turn with a listener: %v", err)
	}
	requireLifecycleKinds(t, events, []string{contracts.BackendKindNotice}, "the resume-lost notice waits for the first turn that can receive it")
	assertSpawnResumes(t, spawnedArgvs(t, log), []string{""})
	mustClose(t, r)
}
