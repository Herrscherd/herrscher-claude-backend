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
	models := Models()
	for index := range models {
		if models[index].Route == contracts.RouteGateway {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no gateway models declared; the public build would have an empty catalog")
	}
}

func TestGatewayAndNativeIDsDoNotCollide(t *testing.T) {
	seen := make(map[string]contracts.Route)
	models := Models()
	for index := range models {
		if prev, ok := seen[models[index].ID]; ok {
			t.Fatalf("ID %q used by both route %q and %q", models[index].ID, prev, models[index].Route)
		}
		seen[models[index].ID] = models[index].Route
	}
}

func TestModelsCarryEfforts(t *testing.T) {
	models := Models()
	for index := range models {
		if models[index].Arg == "" || !strings.HasPrefix(models[index].Arg, "claude-") {
			continue
		}
		if len(models[index].Efforts) == 0 {
			t.Errorf("model %q declares no efforts; claude has a separate effort axis", models[index].ID)
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
	models := Models()
	for index := range models {
		route, ok := want[models[index].ID]
		if !ok {
			continue
		}
		if models[index].Route != route || models[index].Arg != models[index].ID {
			t.Errorf("model %q: route %q arg %q, want route %q and arg %q", models[index].ID, models[index].Route, models[index].Arg, route, models[index].ID)
		}
		delete(want, models[index].ID)
	}
	for id := range want {
		t.Errorf("model %q missing from the catalog", id)
	}
}

type publishedPricing struct {
	input, output, cacheRead, cacheWrite float64
	window                               int
}

func TestNativePricingFollowsPublishedTable(t *testing.T) {
	want := map[string]publishedPricing{
		"claude-opus-5-5":    {input: 4, output: 20, cacheRead: 0.2, cacheWrite: 5, window: 1_000_000},
		"claude-opus-5-5-1m": {input: 4, output: 20, cacheRead: 0.2, cacheWrite: 5, window: 1_000_000},
		"claude-fable-5-1":   {input: 10, output: 50, cacheRead: 0.25, cacheWrite: 12.5, window: 1_000_000},
		"claude-opus-5":      {input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25, window: 1_000_000},
		"claude-fable-5":     {input: 10, output: 50, cacheRead: 1, cacheWrite: 12.5, window: 1_000_000},
		"claude-sonnet-5":    {input: 2, output: 10, cacheRead: 0.2, cacheWrite: 2.5, window: 1_000_000},
		"claude-sonnet-5-5":  {input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5, window: 1_000_000},
		"claude-haiku-5-5":   {input: 0.1, output: 0.5, cacheRead: 0.01, cacheWrite: 0.125, window: 1_000_000},
		"claude-haiku-4-5":   {input: 1, output: 5, cacheRead: 0.1, cacheWrite: 1.25, window: 200_000},
		"claude-opus-4-8":    {input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25, window: 1_000_000},
		"claude-opus-4-8-1m": {input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25, window: 1_000_000},
	}
	models := Models()
	for index := range models {
		spec := models[index]
		if spec.Route != contracts.RouteNative {
			continue
		}
		price, ok := want[spec.ID]
		if !ok {
			t.Errorf("native model %q has no published pricing in the test table", spec.ID)
			continue
		}
		got := publishedPricing{spec.InputPrice, spec.OutputPrice, spec.CacheReadPrice, spec.CacheWritePrice, spec.ContextWindow}
		if got != price {
			t.Errorf("model %q pricing = %+v, published %+v", spec.ID, got, price)
		}
		delete(want, spec.ID)
	}
	for id := range want {
		t.Errorf("published model %q missing from the native catalog", id)
	}
}

func TestGatewayEntriesCarryNoPrice(t *testing.T) {
	models := Models()
	for index := range models {
		gateway := models[index]
		if gateway.Route != contracts.RouteGateway {
			continue
		}
		if gateway.InputPrice != 0 || gateway.OutputPrice != 0 || gateway.CacheReadPrice != 0 || gateway.CacheWritePrice != 0 {
			t.Errorf("gateway model %q declares a price; the gateway price is ours and is not published in this catalog", gateway.ID)
		}
	}
}

func TestGatewayEntriesKeepTheirNativeWindow(t *testing.T) {
	models := Models()
	for index := range models {
		gateway := models[index]
		if gateway.Route != contracts.RouteGateway {
			continue
		}
		native, ok := nativeTwin(models, gateway.Arg)
		if !ok {
			if gateway.ContextWindow != 0 {
				t.Errorf("gateway model %q has no native twin yet declares window %d", gateway.ID, gateway.ContextWindow)
			}
			continue
		}
		if gateway.ContextWindow != native.ContextWindow {
			t.Errorf("gateway model %q window = %d, native %q window = %d", gateway.ID, gateway.ContextWindow, native.ID, native.ContextWindow)
		}
	}
}

func nativeTwin(models []contracts.ModelSpec, arg string) (contracts.ModelSpec, bool) {
	for index := range models {
		if models[index].Route == contracts.RouteNative && models[index].Arg == arg {
			return models[index], true
		}
	}
	return contracts.ModelSpec{}, false
}

func TestSupersededModelsAreHidden(t *testing.T) {
	hidden := map[string]bool{
		"claude-opus-5":       true,
		"claude-fable-5":      true,
		"claude-sonnet-5":     true,
		"claude-haiku-4-5":    true,
		"claude-opus-4-8":     true,
		"claude-opus-4-8-1m":  true,
		"gw-claude-opus-5":    true,
		"gw-claude-sonnet-5":  true,
		"gw-claude-haiku-4-5": true,
	}
	models := Models()
	for index := range models {
		spec := models[index]
		if spec.Hidden != hidden[spec.ID] {
			t.Errorf("model %q hidden = %v, want %v", spec.ID, spec.Hidden, hidden[spec.ID])
		}
	}
}
