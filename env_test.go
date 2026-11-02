package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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
		defer mustClose(t, sess)

		got := waitForProbeFile(t, probeFile)
		if got != "injected-stream-value" {
			t.Fatalf("probe file contains %q, want %q", got, "injected-stream-value")
		}
	})

	t.Run("nil env leaves the probe var absent", func(t *testing.T) {
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
		defer mustClose(t, sess)

		got := waitForProbeFile(t, probeFile)
		if got != "ABSENT" {
			t.Fatalf("probe variable leaked into the child environment with nil injection: %q", got)
		}
	})
}

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
	if _, err := NewBackend(context.Background(), Config{Kind: "oneshot", Cmd: "sh -c env", Env: map[string]string{
		contracts.EnvAnthropicAPIKey: "sk-token",
	}}); err != nil {
		t.Fatalf("a lone token (nothing redirected) was refused: %v", err)
	}
}

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
			t.Fatalf("the native route scrubbed a variable it must leave alone: this would regress today's internal build:\n%s", out)
		}
	})
}
