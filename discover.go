package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	anthropicVersion   = "2026-10-21"
	apiVersionHeader   = "Anthropic-Version"
	modelsPageLimit    = "1000"
	modelsMaxPages     = 20
	modelsMaxBodyBytes = 4 << 20
	modelsLifecycle    = "active"
	nativeModelType    = "model"
	claudeIDPrefix     = "claude-"
	gatewayIDPrefix    = "gw-"
)

const anthropicAPIBaseURL = "https://api.anthropic.com"

var (
	errModelsStatus     = errors.New("models endpoint refused the request")
	errModelsPagination = errors.New("models endpoint paginates without a cursor")
	errModelsTooMany    = errors.New("models endpoint has more pages than the limit")
	errModelsRedirect   = errors.New("models endpoint redirected, the key is not resent")
)

type modelsPage struct {
	Data    []apiModel `json:"data"`
	HasMore bool       `json:"has_more"`
	LastID  string     `json:"last_id"`
}

type apiModel struct {
	ID           string          `json:"id"`
	DisplayName  string          `json:"display_name"`
	Type         string          `json:"type"`
	Lifecycle    string          `json:"lifecycle"`
	Capabilities apiCapabilities `json:"capabilities"`
}

type apiCapabilities struct {
	Effort apiEffort `json:"effort"`
}

type apiEffort struct {
	Supported bool        `json:"supported"`
	Low       effortLevel `json:"low"`
	Medium    effortLevel `json:"medium"`
	High      effortLevel `json:"high"`
	Xhigh     effortLevel `json:"xhigh"`
	Max       effortLevel `json:"max"`
}

type effortLevel struct {
	Supported bool `json:"supported"`
}

func discover(ctx context.Context, access contracts.DiscoveryAccess) ([]contracts.ModelSpec, error) {
	return discoverFrom(ctx, access, newModelsClient(), anthropicAPIBaseURL)
}

func newModelsClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errModelsRedirect
		},
	}
}

func discoverFrom(ctx context.Context, access contracts.DiscoveryAccess, client *http.Client, nativeBaseURL string) ([]contracts.ModelSpec, error) {
	if access.Gateway.BaseURL() != "" && access.Gateway.Token() != "" {
		return listModels(ctx, client, access.Gateway.BaseURL(), access.Gateway.Token(), contracts.RouteGateway)
	}
	key := strings.TrimSpace(access.Getenv(contracts.EnvAnthropicAPIKey))
	if key == "" {
		return nil, nil
	}
	return listModels(ctx, client, nativeBaseURL, key, contracts.RouteNative)
}

func listModels(ctx context.Context, client *http.Client, baseURL, key string, route contracts.Route) ([]contracts.ModelSpec, error) {
	var specs []contracts.ModelSpec
	seen := make(map[string]bool)
	afterID := ""
	for page := 0; ; page++ {
		if page == modelsMaxPages {
			return nil, errModelsTooMany
		}
		body, err := fetchModelsPage(ctx, client, baseURL, key, afterID)
		if err != nil {
			return nil, err
		}
		for _, model := range body.Data {
			spec, ok := specFromAPI(model, route)
			if !ok || seen[spec.ID] {
				continue
			}
			seen[spec.ID] = true
			specs = append(specs, spec)
		}
		if !body.HasMore {
			return specs, nil
		}
		if body.LastID == "" {
			return nil, errModelsPagination
		}
		afterID = body.LastID
	}
}

func fetchModelsPage(ctx context.Context, client *http.Client, baseURL, key, afterID string) (modelsPage, error) {
	query := url.Values{}
	query.Set("limit", modelsPageLimit)
	query.Set("lifecycle", modelsLifecycle)
	if afterID != "" {
		query.Set("after_id", afterID)
	}
	endpoint := strings.TrimSuffix(baseURL, "/") + "/v1/models?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return modelsPage{}, fmt.Errorf("build models request: %w", err)
	}
	request.Header.Set("X-Api-Key", key)
	request.Header.Set(apiVersionHeader, anthropicVersion)
	response, err := client.Do(request)
	if err != nil {
		return modelsPage{}, fmt.Errorf("list models: %w", err)
	}
	return readModelsResponse(response)
}

func readModelsResponse(response *http.Response) (modelsPage, error) {
	body, readErr := decodeModelsPage(response)
	if closeErr := response.Body.Close(); closeErr != nil && readErr == nil {
		return modelsPage{}, fmt.Errorf("close models response: %w", closeErr)
	}
	return body, readErr
}

func decodeModelsPage(response *http.Response) (modelsPage, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return modelsPage{}, fmt.Errorf("%w: %s", errModelsStatus, response.Status)
	}
	var body modelsPage
	if err := json.NewDecoder(io.LimitReader(response.Body, modelsMaxBodyBytes)).Decode(&body); err != nil {
		return modelsPage{}, fmt.Errorf("decode models page: %w", err)
	}
	return body, nil
}

func specFromAPI(model apiModel, route contracts.Route) (contracts.ModelSpec, bool) {
	if model.Type != nativeModelType || !strings.HasPrefix(model.ID, claudeIDPrefix) {
		return contracts.ModelSpec{}, false
	}
	if model.Lifecycle != "" && model.Lifecycle != modelsLifecycle {
		return contracts.ModelSpec{}, false
	}
	spec := contracts.ModelSpec{
		ID:      model.ID,
		Label:   model.DisplayName,
		Arg:     model.ID,
		Efforts: efforts(model.Capabilities.Effort),
		Route:   route,
	}
	if route == contracts.RouteGateway {
		spec.ID = gatewayIDPrefix + model.ID
	}
	return spec, true
}

func efforts(effort apiEffort) []string {
	if !effort.Supported {
		return nil
	}
	levels := []struct {
		name      string
		supported bool
	}{
		{"low", effort.Low.Supported},
		{"medium", effort.Medium.Supported},
		{"high", effort.High.Supported},
		{"xhigh", effort.Xhigh.Supported},
		{"max", effort.Max.Supported},
	}
	var names []string
	for _, level := range levels {
		if level.supported {
			names = append(names, level.name)
		}
	}
	return names
}
