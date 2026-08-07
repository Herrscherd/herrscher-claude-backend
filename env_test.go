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

// TestNativeSpawnInheritsAndInjectionReplaces exercises the two spawn-time
// environment properties through a real child process, not through
// contracts.MergeEnv directly: on the native route the child sees the daemon's
// own environment, and on the gateway route an injected key wins over the
// inherited entry. Letting the inherited value win — or dropping the merge —
// would run the session on the machine's own claude.ai subscription while the
// system believes it injected a gateway credential, which is the silent
// failure the whole env path exists to prevent.
func TestNativeSpawnInheritsAndInjectionReplaces(t *testing.T) {
	const probeVar = "HERRSCHER_ENV_PROBE"
	script := `printf '%s' "$` + probeVar + `"`

	t.Run("native spawn inherits the daemon environment", func(t *testing.T) {
		t.Setenv(probeVar, "inherited")
		got, err := runCmd(context.Background(), "sh -c", nil, contracts.Prompt{Content: script})
		if err != nil {
			t.Fatal(err)
		}
		if got != "inherited" {
			t.Fatalf("native child resolved %q, want inherited", got)
		}
	})

	t.Run("injection replaces the inherited value", func(t *testing.T) {
		t.Setenv(probeVar, "inherited")
		got, err := runCmd(context.Background(), "sh -c", map[string]string{probeVar: "injected"}, contracts.Prompt{Content: script})
		if err != nil {
			t.Fatal(err)
		}
		if got != "injected" {
			t.Fatalf("injected child resolved %q, want injected", got)
		}
	})
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
			contracts.EnvAnthropicBaseURL: "https://gw.example",
			contracts.EnvAnthropicAPIKey:  "",
		}},
		{"token whitespace", map[string]string{
			contracts.EnvAnthropicBaseURL: "https://gw.example",
			contracts.EnvAnthropicAPIKey:  "  \t ",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, kind := range []string{"oneshot", "stream"} {
				b, err := NewBackend(context.Background(), Config{Kind: kind, Cmd: "sh -c env", Env: tc.env})
				if err == nil {
					_ = b
					t.Fatalf("kind %q: NewBackend accepted a base URL with no token", kind)
				}
				if !strings.Contains(err.Error(), contracts.EnvAnthropicAPIKey) {
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
		contracts.EnvAnthropicBaseURL: "https://gw.example",
		contracts.EnvAnthropicAPIKey:  "sk-token",
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
		contracts.EnvAnthropicAPIKey: "sk-token",
	}}); err != nil {
		t.Fatalf("a lone token (nothing redirected) was refused: %v", err)
	}
}

// TestGatewaySpawnScrubsInheritedLegacyAuthToken is the invariant the leak
// path in spawnEnv exists to close. contracts.MergeEnv only overrides keys
// PRESENT in the injected env, and the host no longer emits
// legacyAnthropicAuthToken — so a value already sitting in the daemon's own
// process environment (a dev shell, a stale systemd unit) would otherwise
// ride into the child untouched, next to the new ANTHROPIC_API_KEY. The
// claude CLI would then send both x-api-key and Authorization, and the API
// answers that with a 401.
//
// t.Setenv pollutes the base environment that runCmd reads via os.Environ() —
// simulating a daemon whose own process environment still carries the legacy
// variable. Without this pollution, a version of spawnEnv that scrubs nothing
// would still pass: the assertion would be vacuously true. The second subtest
// is the other half of the same property: the SAME polluted environment, but
// with no gateway base URL (the native route, where colleagues run the
// internal build on the machine's own login today), must let the inherited
// variable through unchanged — proving the scrub is conditional on the
// gateway route, not unconditional.
func TestGatewaySpawnScrubsInheritedLegacyAuthToken(t *testing.T) {
	t.Run("gateway route: the inherited legacy token does not reach the child", func(t *testing.T) {
		t.Setenv(legacyAnthropicAuthToken, "leaked-daemon-token")

		b, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env", Env: map[string]string{
			contracts.EnvAnthropicBaseURL: "https://gw.example",
			contracts.EnvAnthropicAPIKey:  "sk-token",
		}})
		if err != nil {
			t.Fatalf("NewBackend: %v", err)
		}
		out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
		if err != nil {
			t.Fatalf("Respond: %v (output %q)", err, out)
		}
		if strings.Contains(out, legacyAnthropicAuthToken+"=") {
			t.Fatalf("the daemon's own %s leaked into the gateway-routed child:\n%s", legacyAnthropicAuthToken, out)
		}
		if !strings.Contains(out, contracts.EnvAnthropicAPIKey+"=sk-token") {
			t.Fatalf("the gateway credential itself is missing from the child environment:\n%s", out)
		}
	})

	t.Run("native route: the same pollution passes through unchanged", func(t *testing.T) {
		t.Setenv(legacyAnthropicAuthToken, "leaked-daemon-token")

		b, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env"})
		if err != nil {
			t.Fatalf("NewBackend: %v", err)
		}
		out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
		if err != nil {
			t.Fatalf("Respond: %v (output %q)", err, out)
		}
		if !strings.Contains(out, legacyAnthropicAuthToken+"=leaked-daemon-token") {
			t.Fatalf("the native route scrubbed a variable it must leave alone — this would regress today's internal build:\n%s", out)
		}
	})
}
