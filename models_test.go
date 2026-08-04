package claude

import (
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestModelsAreValid(t *testing.T) {
	if err := contracts.ValidateModels("claude", Models); err != nil {
		t.Fatalf("claude model catalog is invalid: %v", err)
	}
}

func TestModelsAreAllNativeForNow(t *testing.T) {
	// Gateway entries arrive in Task 10. Until they do, this backend must only
	// declare native models — otherwise the host would offer a model that no
	// credential can serve.
	for _, m := range Models {
		if m.Route != contracts.RouteNative {
			t.Errorf("model %q has route %q, expected native at this stage", m.ID, m.Route)
		}
	}
}

func TestModelsCarryEfforts(t *testing.T) {
	for _, m := range Models {
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
