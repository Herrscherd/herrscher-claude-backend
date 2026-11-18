package claude

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	resumeNeverAcceptedCLI       = `log=$1; shift; echo "$*" >> "$log"; case "$*" in *--resume*) read line; exit 1;; esac; read line; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`
	silentDeathsAfterAcceptedCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -eq 1 ]; then printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"first","session_id":"sid-old"}'; read line; exit 1; fi; if [ "$n" -eq 2 ]; then exit 1; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"done","session_id":"sid-new"}'`
	refusedAfterAcceptedCLI      = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; case "$n" in 1) printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"first","session_id":"sid-old"}'; read line; exit 1;; 2) printf '%s\n' '{"type":"result","is_error":true,"subtype":"error_during_execution","session_id":"sid-old","errors":["No conversation found with session ID: sid-old"]}'; exit 1;; esac; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-new"}' '{"type":"result","is_error":false,"result":"fresh","session_id":"sid-new"}'`
)

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
	if want := []string{contracts.BackendKindNotice, "reset"}; !slices.Equal(lifecycleKinds(events), want) {
		t.Fatalf("lifecycle events = %q, want %q: a lost resume gives one notice, then the reset", lifecycleKinds(events), want)
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
	requireLostResumeEvents(t, events)
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
	requireLostResumeEvents(t, events)
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

func TestSilentDeathAfterAnAcceptedResumeEmitsNeitherNoticeNorReset(t *testing.T) {
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
	if kinds := lifecycleKinds(events); len(kinds) != 0 {
		t.Fatalf("lifecycle events = %q, want none: an accepted resume that dies once resumes silently", kinds)
	}
	mustClose(t, r)
}
