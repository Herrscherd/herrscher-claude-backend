package claude

import (
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestModelsAreValid(t *testing.T) {
	if err := contracts.ValidateModels("claude", Models()); err != nil {
		t.Fatalf("claude model catalog is invalid: %v", err)
	}
}

func TestGatewayModelsExist(t *testing.T) {
	var n int
	for _, m := range Models() {
		if m.Route == contracts.RouteGateway {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no gateway models declared; the public build would have an empty catalog")
	}
}

func TestGatewayAndNativeIDsDoNotCollide(t *testing.T) {
	seen := make(map[string]contracts.Route)
	for _, m := range Models() {
		if prev, ok := seen[m.ID]; ok {
			t.Fatalf("ID %q used by both route %q and %q", m.ID, prev, m.Route)
		}
		seen[m.ID] = m.Route
	}
}

func TestModelsCarryEfforts(t *testing.T) {
	for _, m := range Models() {
		if m.Arg == "" || !strings.HasPrefix(m.Arg, "claude-") {
			continue
		}
		if len(m.Efforts) == 0 {
			t.Errorf("model %q declares no efforts; claude has a separate effort axis", m.ID)
		}
	}
}

func TestManifestPublishesModels(t *testing.T) {
	var found bool
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind != "claude" {
			continue
		}
		found = true
		if len(p.Manifest.Models) != len(Models()) {
			t.Fatalf("manifest published %d models, catalog has %d", len(p.Manifest.Models), len(Models()))
		}
	}
	if !found {
		t.Fatal("claude backend did not self-register")
	}
}

func TestCurrentModelsAreNativeWithTheirOwnArg(t *testing.T) {
	want := map[string]contracts.Route{
		"claude-sonnet-5-5": contracts.RouteNative,
		"claude-haiku-5-5":  contracts.RouteNative,
	}
	for _, m := range Models() {
		route, ok := want[m.ID]
		if !ok {
			continue
		}
		if m.Route != route || m.Arg != m.ID {
			t.Errorf("model %q: route %q arg %q, want route %q and arg %q", m.ID, m.Route, m.Arg, route, m.ID)
		}
		delete(want, m.ID)
	}
	for id := range want {
		t.Errorf("model %q missing from the catalog", id)
	}
}
