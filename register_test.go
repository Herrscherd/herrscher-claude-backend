package claude

import (
	"context"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func backendPlugin(t *testing.T) contracts.Plugin {
	t.Helper()
	backends := contracts.Default.Backends()
	for i := range backends {
		if backends[i].Manifest.Kind == "claude" {
			return backends[i]
		}
	}
	t.Fatal("claude backend did not self-register into contracts.Default")
	return contracts.Plugin{}
}

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

func TestFactoryCarriesEnvSettingToTheChild(t *testing.T) {
	p := backendPlugin(t)
	if p.Backend == nil {
		t.Fatal("registered claude plugin has a nil backend factory")
	}
	setting := contracts.EncodeEnvSetting(map[string]string{"HERRSCHER_ENV_PROBE": "injected"})

	b, err := p.Backend(context.Background(), contracts.PluginConfig{Settings: map[string]string{
		"kind": "oneshot",
		"cmd":  "sh -c env",
		"env":  setting,
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

func TestEnvSettingCannotLeakTheDaemonEnvironment(t *testing.T) {
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

func TestDeclaresPerToolCallGateGrain(t *testing.T) {
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
