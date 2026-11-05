package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Gateway: creds, Policy: contracts.PolicyGatewayOnly, Getenv: keyEnv("sk-ignored")}, server.Client(), "http://unused.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gw-claude-opus-6" || models[0].Arg != "claude-opus-6" || models[0].Route != contracts.RouteGateway {
		t.Fatalf("gateway models = %+v", models)
	}
}

func countingModelsServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writeBody(t, w, body)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestDiscoverAllPolicyListsNativeAndGateway(t *testing.T) {
	gateway, _ := countingModelsServer(t, `{"data":[{"id":"claude-opus-6","display_name":"Opus 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-opus-6"}`)
	native, _ := countingModelsServer(t, `{"data":[{"id":"claude-sonnet-6","display_name":"Sonnet 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-sonnet-6"}`)
	creds, err := contracts.NewGatewayCreds(gateway.URL, "gw-token")
	if err != nil {
		t.Fatal(err)
	}

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Gateway: creds, Policy: contracts.PolicyAll, Getenv: keyEnv("sk-test")}, native.Client(), native.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gw-claude-opus-6" || models[0].Route != contracts.RouteGateway || models[1].ID != "claude-sonnet-6" || models[1].Route != contracts.RouteNative {
		t.Fatalf("models = %+v, want the gateway model then the native one", models)
	}
}

func TestDiscoverGatewayOnlyWithCredentialsNeverCallsNative(t *testing.T) {
	gateway, _ := countingModelsServer(t, `{"data":[{"id":"claude-opus-6","display_name":"Opus 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-opus-6"}`)
	native, nativeHits := countingModelsServer(t, `{"data":[{"id":"claude-sonnet-6","display_name":"Sonnet 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-sonnet-6"}`)
	creds, err := contracts.NewGatewayCreds(gateway.URL, "gw-token")
	if err != nil {
		t.Fatal(err)
	}

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Gateway: creds, Policy: contracts.PolicyGatewayOnly, Getenv: keyEnv("sk-test")}, native.Client(), native.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Route != contracts.RouteGateway {
		t.Fatalf("models = %+v, want only the gateway model", models)
	}
	if nativeHits.Load() != 0 {
		t.Fatalf("native endpoint called %d time(s) under gateway-only", nativeHits.Load())
	}
}

func TestDiscoverGatewayOnlyWithoutCredentialsListsNothing(t *testing.T) {
	native, nativeHits := countingModelsServer(t, `{"data":[{"id":"claude-sonnet-6","display_name":"Sonnet 6","type":"model","lifecycle":"active"}],"has_more":false,"last_id":"claude-sonnet-6"}`)

	models, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Policy: contracts.PolicyGatewayOnly, Getenv: keyEnv("sk-test")}, native.Client(), native.URL)
	if err != nil || models != nil {
		t.Fatalf("discover = %v, %v; want nil, nil", models, err)
	}
	if nativeHits.Load() != 0 {
		t.Fatalf("native endpoint called %d time(s) under gateway-only", nativeHits.Load())
	}
}

func TestRedirectGuardMatchesHostIgnoringCase(t *testing.T) {
	origin := mustRequest(t, "https://Api.Example.com/v1/models")
	sameHost := mustRequest(t, "https://api.example.com/v1/moved")
	if err := keepKeyOnSameHostHTTPS(sameHost, []*http.Request{origin}); err != nil {
		t.Fatalf("same host, other case refused: %v", err)
	}
}

func TestRedirectGuardComparesPortExactly(t *testing.T) {
	origin := mustRequest(t, "https://api.example.com/v1/models")
	otherPort := mustRequest(t, "https://api.example.com:8443/v1/moved")
	if err := keepKeyOnSameHostHTTPS(otherPort, []*http.Request{origin}); !errors.Is(err, errModelsRedirect) {
		t.Fatalf("other port accepted: %v", err)
	}
}

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	return request
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

func tlsModelsClient(server *httptest.Server) *http.Client {
	client := newModelsClient()
	client.Transport = server.Client().Transport
	return client
}

func TestDiscoverFollowsSameHostHTTPSRedirectKeepingKey(t *testing.T) {
	var keySeen atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.Redirect(w, r, "/v1/moved?"+r.URL.RawQuery, http.StatusTemporaryRedirect)
		case "/v1/moved":
			if r.Header.Get("X-Api-Key") == "sk-test" {
				keySeen.Add(1)
			}
			writeBody(t, w, `{"data":[],"has_more":false,"last_id":""}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Getenv: keyEnv("sk-test")}, tlsModelsClient(server), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if keySeen.Load() != 1 {
		t.Fatalf("key reached the same-host redirect target %d time(s), want 1", keySeen.Load())
	}
}

func TestDiscoverNeverResendsKeyOnRedirect(t *testing.T) {
	var leaked atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			leaked.Add(1)
		}
		writeBody(t, w, `{"data":[],"has_more":false,"last_id":""}`)
	}))
	defer elsewhere.Close()
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	_, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Getenv: keyEnv("sk-test")}, tlsModelsClient(redirecting), redirecting.URL)
	if !errors.Is(err, errModelsRedirect) {
		t.Fatalf("err = %v, want errModelsRedirect", err)
	}
	if leaked.Load() != 0 {
		t.Fatalf("key sent to a redirect target %d time(s)", leaked.Load())
	}
}

func TestDiscoverNeverFollowsRedirectToPlainHTTP(t *testing.T) {
	var leaked atomic.Int32
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	client := tlsModelsClient(redirecting)
	transport := client.Transport
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme == "http" && request.Header.Get("X-Api-Key") != "" {
			leaked.Add(1)
		}
		return transport.RoundTrip(request)
	})

	_, err := discoverFrom(context.Background(), contracts.DiscoveryAccess{Getenv: keyEnv("sk-test")}, client, redirecting.URL)
	if !errors.Is(err, errModelsRedirect) {
		t.Fatalf("err = %v, want errModelsRedirect", err)
	}
	if leaked.Load() != 0 {
		t.Fatalf("key sent over plain http %d time(s)", leaked.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type trackedBody struct {
	io.Reader

	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestDiscoverClosesBodyOnErrorStatus(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader("overloaded")}
	client := newModelsClient()
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: body, Request: r}, nil
	})

	_, err := listModels(context.Background(), client, "http://models.invalid", "sk-test", contracts.RouteNative)
	if !errors.Is(err, errModelsStatus) {
		t.Fatalf("err = %v, want errModelsStatus", err)
	}
	if !body.closed {
		t.Fatal("response body left open on an error status")
	}
}

func TestDiscoverPageLimitIsAnError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		next := requests.Add(1)
		writeBody(t, w, fmt.Sprintf(`{"data":[],"has_more":true,"last_id":"page-%d"}`, next))
	}))
	defer server.Close()

	_, err := listModels(context.Background(), server.Client(), server.URL, "sk-test", contracts.RouteNative)
	if !errors.Is(err, errModelsTooMany) {
		t.Fatalf("err = %v, want errModelsTooMany", err)
	}
	if requests.Load() != modelsMaxPages {
		t.Fatalf("requests = %d, want %d", requests.Load(), modelsMaxPages)
	}
}

func TestDiscoverPageOverBodyCapIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"data":[],"has_more":false,"last_id":"`+strings.Repeat("a", modelsMaxBodyBytes)+`"}`)
	}))
	defer server.Close()

	_, err := listModels(context.Background(), server.Client(), server.URL, "sk-test", contracts.RouteNative)
	if err == nil {
		t.Fatal("oversized page accepted")
	}
}
