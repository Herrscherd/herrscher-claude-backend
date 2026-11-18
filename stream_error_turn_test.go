package claude

import (
	"context"
	"path/filepath"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const errorTurnThenExitCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -eq 1 ]; then printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":true,"subtype":"error_during_execution","result":"boom","session_id":"sid-old"}'; exit 0; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"ok","session_id":"sid-old"}'`

func TestSessionThatEndedItsTurnInErrorIsRelaunchedWithResumeWithoutResetOrDeath(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, errorTurnThenExitCLI), "", "", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err == nil {
		t.Fatal("the first turn: the CLI answers an error result, want an error")
	}
	var events []contracts.BackendEvent
	reply, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, recordEvents(&events))
	if err != nil || reply != "ok" {
		t.Fatalf("reply %q, err %v: the session that ended its turn in error must be relaunched with --resume and answer", reply, err)
	}
	requireLifecycleKinds(t, events, nil, "an error turn ends its session, so the next turn relaunches without a reset or a notice: the process is not a silent death")
	assertSpawnResumes(t, spawnedArgvs(t, log), []string{"", "--resume sid-old"})
	mustClose(t, r)
}
