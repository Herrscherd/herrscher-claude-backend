package claude

import (
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestModelsAreValid(t *testing.T) {
	if err := contracts.ValidateModels("claude", Models); err != nil {
		t.Fatalf("claude model catalog is invalid: %v", err)
	}
}

func TestGatewayModelsExist(t *testing.T) {
	var n int
	for _, m := range Models {
		if m.Route == contracts.RouteGateway {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no gateway models declared; the public build would have an empty catalog")
	}
}

func TestGatewayAndNativeIDsDoNotCollide(t *testing.T) {
	// The same model served by two routes must have two distinct IDs: the ID
	// is what determines the route at resume.
	seen := map[string]contracts.Route{}
	for _, m := range Models {
		if prev, ok := seen[m.ID]; ok {
			t.Fatalf("ID %q used by both route %q and %q", m.ID, prev, m.Route)
		}
		seen[m.ID] = m.Route
	}
}

func TestModelsCarryEfforts(t *testing.T) {
	// This only applies to actual Claude models: third-party models on the
	// Anthropic protocol (GLM, Qwen, DeepSeek) have no separate effort axis,
	// and passing one through would produce a flag the upstream rejects.
	for _, m := range Models {
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
		if len(p.Manifest.Models) != len(Models) {
			t.Fatalf("manifest published %d models, catalog has %d", len(p.Manifest.Models), len(Models))
		}
	}
	if !found {
		t.Fatal("claude backend did not self-register")
	}
}
