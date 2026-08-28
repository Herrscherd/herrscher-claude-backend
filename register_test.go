package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

// backendPlugin returns the claude plugin as the host sees it: from the global
// registry, not from a struct the test built itself.
func backendPlugin(t *testing.T) contracts.Plugin {
	t.Helper()
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind == "claude" {
			return p
		}
	}
	t.Fatal("claude backend did not self-register into contracts.Default")
	return contracts.Plugin{}
}

// envSetting returns the manifest's declaration of the "env" setting.
func envSetting(t *testing.T) contracts.Setting {
	t.Helper()
	for _, s := range backendPlugin(t).Manifest.Config {
		if s.Key == "env" {
			return s
		}
	}
	t.Fatal(`the manifest declares no "env" setting: the host cannot inject gateway credentials`)
	return contracts.Setting{}
}

// TestFactoryCarriesEnvSettingToTheChild walks the WHOLE host path: the
// registered factory, a PluginConfig whose settings carry an encoded "env"
// value, and a real spawn that reports what it actually received.
//
// Every other env test in this repo builds Config{Env: ...} by hand, which
// leaves the one line that connects the two halves —
// contracts.ParseEnvSetting(cfg.Get("env")) in register.go — completely
// unpinned: delete it and the suite stays green. In production that means the
// host resolves gateway credentials, encodes them, and every session spawns
// with the bare native environment while being billed as gateway.
func TestFactoryCarriesEnvSettingToTheChild(t *testing.T) {
	p := backendPlugin(t)
	if p.Backend == nil {
		t.Fatal("registered claude plugin has a nil backend factory")
	}
	// The host encodes with contracts.EncodeEnvSetting; decoding it is exactly
	// what register.go is responsible for.
	setting := contracts.EncodeEnvSetting(map[string]string{"HERRSCHER_ENV_PROBE": "injected"})

	b, err := p.Backend(context.Background(), contracts.PluginConfig{Settings: map[string]string{
		"kind": "oneshot",
		// "sh -c env", not bare "env": runCmd appends the prompt content as a
		// final positional argument, which plain env would try to exec.
		"cmd": "sh -c env",
		"env": setting,
	}})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	out, err := b.Respond(context.Background(), contracts.Prompt{Content: "x"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "HERRSCHER_ENV_PROBE=injected") {
		t.Fatalf("the \"env\" setting never reached the child environment:\n%s", out)
	}
}

// TestEnvSettingIsNotEnvBound pins the deliberate Env:"" on the "env" setting.
// Binding it to CLAUDE_ENV (or giving it a Default) would let a single
// machine-local variable inject arbitrary environment into EVERY session,
// entirely outside the host's GatewayCreds gate — including an
// ANTHROPIC_BASE_URL pointing at a third-party endpoint with the user's own
// login attached. This setting is host-supplied per session or it is absent.
func TestEnvSettingIsNotEnvBound(t *testing.T) {
	s := envSetting(t)
	if s.Env != "" {
		t.Errorf(`the "env" setting is bound to %q; it must never be readable from the daemon environment`, s.Env)
	}
	if s.Default != "" {
		t.Errorf(`the "env" setting has default %q; a default would inject environment into every session`, s.Default)
	}
}

func TestSelfRegisteredAsBackend(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind == "claude" {
			if p.Backend == nil {
				t.Fatal("registered claude plugin has a nil backend factory")
			}
			return
		}
	}
	t.Fatal("claude backend did not self-register into contracts.Default")
}

// The host materializes the PreToolUse hook only for a backend that declares
// this grain, so losing it would leave every claude session running ungated
// while the operator still sees the mode they asked for.
func TestDeclaresPerToolGating(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != "claude" {
			continue
		}
		if p.Manifest.Capabilities.Gate != contracts.GrainTool {
			t.Fatalf("gate = %q, want tool", p.Manifest.Capabilities.Gate)
		}
		return
	}
	t.Fatal("claude backend did not self-register into contracts.Default")
}
