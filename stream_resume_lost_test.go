package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const resumeLostAfterAcceptedTurnCLI = `log=$1; shift; echo "$*" >> "$log"; n=$(wc -l < "$log"); read line; if [ "$n" -gt 1 ]; then printf '%s\n' '{"type":"result","is_error":false,"result":"done","session_id":"sid-old"}'; exit 0; fi; printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-old"}' '{"type":"result","is_error":false,"result":"first","session_id":"sid-old"}'; read line; exit 1`

func TestResumeAcceptedInAnEarlierTurnIsNotForgotten(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "spawns.log")
	r := newStreamResponder(context.Background(), fakeCLIBase(log, resumeLostAfterAcceptedTurnCLI), "", "sid-old", "", nil)

	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "first"}, nil); err != nil {
		t.Fatalf("first turn, the resume is accepted: %v", err)
	}
	if _, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "second"}, nil); err != nil {
		t.Fatalf("second turn, the CLI dies before any line: %v", err)
	}
	argvs := spawnedArgvs(t, log)
	if len(argvs) != 2 {
		t.Fatalf("the CLI was spawned %d times, want 2: the first session, then one relaunch", len(argvs))
	}
	for i, argv := range argvs {
		if !strings.Contains(argv, "--resume sid-old") {
			t.Fatalf("spawn %d args = %q, want --resume sid-old: a resume the CLI accepted must not be dropped, the conversation would restart from zero", i+1, argv)
		}
	}
	mustClose(t, r)
}
