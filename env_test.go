package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// waitForProbeFile polls for path to appear (a bounded deadline rather than a
// fixed sleep, since the child writes it asynchronously right after spawn) and
// returns its contents. It fails the test with a clear message if the file
// never shows up within the deadline.
func waitForProbeFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("probe file %s never appeared within the deadline", path)
	return ""
}

// TestStreamSpawnAppliesInjectedEnv closes the gap left by the one-shot-only
// coverage above: startStreamSession (the persistent streaming spawn site) had
// no real-process test, only compile-checked argument threading. If the
// cmd.Env line in startStreamSession were ever dropped, a stream session would
// silently run against the user's own claude.ai environment while the system
// believed it was running on the paid gateway — a state the project's legal
// constraints forbid — and argument threading alone cannot detect that.
//
// startStreamSession execs argv[0] directly (no shell) with argv built as base
// followed by the stream-json flags. Passing base as ["sh", "-c", script] makes
// sh read the script from -c and treat the appended stream flags as positional
// parameters ($0, $1, ...), which the script below never references. Each
// script records what it sees of the probe variable to a file and then execs
// `cat`, so the child blocks on stdin exactly like a real claude stream-json
// process would — startStreamSession returns a live session, not one that has
// already exited.
func TestStreamSpawnAppliesInjectedEnv(t *testing.T) {
	const probeVar = "NEUBLOX_STREAM_PROBE"

	t.Run("injected value reaches the child", func(t *testing.T) {
		dir := t.TempDir()
		probeFile := filepath.Join(dir, "probe.txt")
		script := fmt.Sprintf(`printf '%%s' "$%s" > %q; exec cat`, probeVar, probeFile)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := startStreamSession(ctx, []string{"sh", "-c", script}, "", "", dir, map[string]string{probeVar: "injected-stream-value"})
		if err != nil {
			t.Fatalf("startStreamSession: %v", err)
		}
		defer func() { _ = sess.Close() }()

		got := waitForProbeFile(t, probeFile)
		if got != "injected-stream-value" {
			t.Fatalf("probe file contains %q, want %q", got, "injected-stream-value")
		}
	})

	t.Run("nil env leaves the probe var absent", func(t *testing.T) {
		// Without this case, a passing "injected" test above cannot distinguish
		// real injection from an ambient variable that was already set.
		dir := t.TempDir()
		probeFile := filepath.Join(dir, "probe.txt")
		script := fmt.Sprintf(`if [ -z "${%s+x}" ]; then printf ABSENT > %q; else printf 'SET:%%s' "$%s" > %q; fi; exec cat`,
			probeVar, probeFile, probeVar, probeFile)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := startStreamSession(ctx, []string{"sh", "-c", script}, "", "", dir, nil)
		if err != nil {
			t.Fatalf("startStreamSession: %v", err)
		}
		defer func() { _ = sess.Close() }()

		got := waitForProbeFile(t, probeFile)
		if got != "ABSENT" {
			t.Fatalf("probe variable leaked into the child environment with nil injection: %q", got)
		}
	})
}

// TestHalfGatewayPairRefusesToSpawn is the plugin-side fail-closed check. A
// base URL with no token makes the claude CLI talk to the gateway while
// authenticating with the machine's own claude.ai login — the forbidden shape,
// and a silent one: the CLI works and nothing looks wrong. The host makes this
// unrepresentable with GatewayCreds, but the plugin is a separate module whose
// only boundary is a map[string]string, so it must refuse on its own.
func TestHalfGatewayPairRefusesToSpawn(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"token absent", map[string]string{
			contracts.EnvAnthropicBaseURL: "https://gw.example",
		}},
		{"token empty", map[string]string{
			contracts.EnvAnthropicBaseURL:   "https://gw.example",
			contracts.EnvAnthropicAuthToken: "",
		}},
		{"token whitespace", map[string]string{
			contracts.EnvAnthropicBaseURL:   "https://gw.example",
			contracts.EnvAnthropicAuthToken: "  \t ",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, kind := range []string{"oneshot", "stream"} {
				b, err := NewBackend(context.Background(), Config{Kind: kind, Cmd: "sh -c env", Env: tc.env})
				if err == nil {
					_ = b
					t.Fatalf("kind %q: NewBackend accepted a base URL with no token", kind)
				}
				if !strings.Contains(err.Error(), contracts.EnvAnthropicAuthToken) {
					t.Errorf("kind %q: error does not name the missing token: %v", kind, err)
				}
			}
		})
	}
}

// TestCompleteGatewayPairIsAccepted and TestNativeEnvIsAccepted are the two
// non-regressions the refusal above must not break: the real gateway route,
// and the internal build's native route with no injection at all.
func TestCompleteGatewayPairIsAccepted(t *testing.T) {
	if _, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env", Env: map[string]string{
		contracts.EnvAnthropicBaseURL:   "https://gw.example",
		contracts.EnvAnthropicAuthToken: "sk-token",
	}}); err != nil {
		t.Fatalf("a complete credential pair was refused: %v", err)
	}
}

func TestNativeEnvIsAccepted(t *testing.T) {
	if _, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env"}); err != nil {
		t.Fatalf("the native route (no injection) was refused: %v", err)
	}
	// A lone token with no base URL is not the dangerous shape: nothing is
	// redirected, so it must not be refused either.
	if _, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env", Env: map[string]string{
		contracts.EnvAnthropicAuthToken: "sk-token",
	}}); err != nil {
		t.Fatalf("a lone token (nothing redirected) was refused: %v", err)
	}
}
