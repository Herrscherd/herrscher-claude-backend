package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func noEnv(string) string { return "" }

func keyEnv(key string) func(string) string {
	return func(name string) string {
		if name == contracts.EnvAnthropicAPIKey {
			return key
		}
		return ""
	}
}

func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestDiscoverWithoutCredentialsListsNothing(t *testing.T) {
	models, err := discover(context.Background(), contracts.DiscoveryAccess{Getenv: noEnv})
	if err != nil || models != nil {
		t.Fatalf("discover = %v, %v; want nil, nil", models, err)
	}
}

func TestDiscoverNativeSendsKeyAndFollowsPages(t *testing.T) {
	var afterIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "sk-test" || r.Header.Get(apiVersionHeader) != anthropicVersion {
			http.Error(w, "headers", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("lifecycle") != "active" {
			http.Error(w, "lifecycle", http.StatusBadRequest)
			return
		}
		afterID := r.URL.Query().Get("after_id")
		afterIDs = append(afterIDs, afterID)
		if afterID == "" {
			writeBody(t, w, `{"data":[{"id":"claude-sonnet-6","display_name":"Sonnet 6","type":"model","lifecycle":"active"}],"has_more":true,"last_id":"claude-sonnet-6"}`)
			return
		}
		writeBody(t, w, `{"data":[{"id":"claude-haiku-6","display_name":"Haiku 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-haiku-6"}`)
	}))
	defer server.Close()

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Getenv: keyEnv("sk-test")}, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeModels(t, models)
	if len(afterIDs) != 2 || afterIDs[0] != "" || afterIDs[1] != "claude-sonnet-6" {
		t.Fatalf("pagination cursors = %v", afterIDs)
	}
}

func assertNativeModels(t *testing.T, models []contracts.ModelSpec) {
	t.Helper()
	if len(models) != 2 || models[0].ID != "claude-sonnet-6" || models[1].ID != "claude-haiku-6" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Route != contracts.RouteNative || models[0].Arg != "claude-sonnet-6" || models[0].Label != "Sonnet 6" {
		t.Fatalf("native model = %+v", models[0])
	}
}

func TestDiscoverGatewayUsesGatewayRouteAndPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "gw-token" {
			http.Error(w, "token", http.StatusUnauthorized)
			return
		}
		writeBody(t, w, `{"data":[{"id":"claude-opus-6","display_name":"Opus 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-opus-6"}`)
	}))
	defer server.Close()
	creds, err := contracts.NewGatewayCreds(server.URL, "gw-token")
	if err != nil {
		t.Fatal(err)
	}

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Gateway: creds, Getenv: keyEnv("sk-ignored")}, server.Client(), "http://unused.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gw-claude-opus-6" || models[0].Arg != "claude-opus-6" || models[0].Route != contracts.RouteGateway {
		t.Fatalf("gateway models = %+v", models)
	}
}

func TestSpecFromAPIKeepsOnlyActiveClaudeModels(t *testing.T) {
	cases := map[string]apiModel{
		"other vendor": {ID: "glm-4.7", Type: "model", Lifecycle: "active"},
		"retired":      {ID: "claude-old", Type: "model", Lifecycle: "retired"},
		"not a model":  {ID: "claude-x", Type: "file", Lifecycle: "active"},
	}
	for name, model := range cases {
		if _, ok := specFromAPI(model, contracts.RouteNative); ok {
			t.Fatalf("%s kept", name)
		}
	}
}

func TestEffortsFollowSupportedLevelsInOrder(t *testing.T) {
	effort := apiEffort{
		Supported: true,
		Low:       effortLevel{Supported: true},
		High:      effortLevel{Supported: true},
		Max:       effortLevel{Supported: true},
	}
	got := efforts(effort)
	want := []string{"low", "high", "max"}
	if len(got) != len(want) {
		t.Fatalf("efforts = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("efforts = %v, want %v", got, want)
		}
	}
	if efforts(apiEffort{}) != nil {
		t.Fatal("unsupported effort returned levels")
	}
}

func TestDiscoverFailureIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Getenv: keyEnv("sk-test")}, server.Client(), server.URL)
	if !errors.Is(err, errModelsStatus) {
		t.Fatalf("err = %v, want errModelsStatus", err)
	}
}

func TestDiscoverPaginationWithoutCursorIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"data":[],"has_more":true,"last_id":""}`)
	}))
	defer server.Close()

	_, err := listModels(context.Background(), server.Client(), server.URL, "sk-test", contracts.RouteNative)
	if !errors.Is(err, errModelsPagination) {
		t.Fatalf("err = %v, want errModelsPagination", err)
	}
}
