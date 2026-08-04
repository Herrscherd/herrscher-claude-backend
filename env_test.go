package claude

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestNativeSpawnEnvIsUnchanged(t *testing.T) {
	// Non-regression for the internal build: with no injection, the child
	// process environment is exactly the daemon's.
	got := contracts.MergeEnv(os.Environ(), nil)
	if len(got) != len(os.Environ()) {
		t.Fatalf("native env has %d entries, os.Environ() has %d", len(got), len(os.Environ()))
	}
}

func TestBackendConfigCarriesEnvToOneShot(t *testing.T) {
	// The oneshot spawn must see Config.Env. We do not launch a real process:
	// we verify the value travels from backend construction to the spawn
	// closure via the system's `env` command.
	// Cmd is "sh -c env", not bare "env": runCmd always appends the prompt
	// content as a final positional argument (existing behavior, unrelated to
	// this task), and "env <content>" would make env try to exec <content> as
	// a command instead of printing the environment. "sh -c env" absorbs that
	// trailing argument as $0 and still runs plain env.
	b, err := NewBackend(context.Background(), Config{
		Kind: "oneshot",
		Cmd:  "sh -c env",
		Env:  map[string]string{"HERRSCHER_ENV_PROBE": "injected"},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "HERRSCHER_ENV_PROBE=injected") {
		t.Fatalf("injected variable absent from the child environment:\n%s", out)
	}
}
