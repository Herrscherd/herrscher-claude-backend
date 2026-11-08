package claude

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	usagePath         = "/api/oauth/usage"
	oauthBetaHeader   = "Anthropic-Beta"
	oauthBeta         = "oauth-2025-04-20"
	usageMaxBodyBytes = 1 << 20
	usageFiveHourKey  = "five_hour"
	usageSevenDayKey  = "seven_day"
	usageWeeklyGroup  = "weekly"
	fullPercent       = 100
	fiveHourSpan      = 5 * time.Hour
	weekSpan          = 7 * 24 * time.Hour
)

var (
	errUsageRefused = newCatalogError(keyUsageRefused)
	errUsageUnknown = newCatalogError(keyUsageUnknown)
)

type usageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type usageLimit struct {
	Group    string      `json:"group"`
	Percent  *float64    `json:"percent"`
	ResetsAt string      `json:"resets_at"`
	Scope    *usageScope `json:"scope"`
}

type usageScope struct {
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

func limits(ctx context.Context, access contracts.ProbeAccess) ([]contracts.Limit, error) {
	return limitsFrom(ctx, access, newUsageClient(), anthropicAPIBaseURL, time.Now())
}

func newUsageClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func limitsFrom(ctx context.Context, access contracts.ProbeAccess, client *http.Client, baseURL string, now time.Time) ([]contracts.Limit, error) {
	token, err := readOAuthToken(probeGetenv(access), now)
	if err != nil {
		return nil, err
	}
	report, err := fetchUsage(ctx, client, baseURL, token)
	if err != nil {
		return nil, limitsUnavailable(err)
	}
	limits, err := limitsFromReport(report)
	if err != nil {
		return nil, limitsUnavailable(err)
	}
	return limits, nil
}

func limitsUnavailable(cause error) error {
	return fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, cause)
}

func fetchUsage(ctx context.Context, client *http.Client, baseURL, token string) (map[string]json.RawMessage, error) {
	endpoint := strings.TrimSuffix(baseURL, "/") + usagePath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build usage request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(oauthBetaHeader, oauthBeta)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read usage: %w", err)
	}
	report, readErr := decodeUsage(response)
	if closeErr := response.Body.Close(); closeErr != nil && readErr == nil {
		return nil, fmt.Errorf("close usage response: %w", closeErr)
	}
	return report, readErr
}

func decodeUsage(response *http.Response) (map[string]json.RawMessage, error) {
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s", errUsageRefused, response.Status)
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		return nil, fmt.Errorf("%w: %s", errUsageRefused, response.Status)
	}
	var report map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, usageMaxBodyBytes)).Decode(&report); err != nil {
		return nil, fmt.Errorf("decode usage: %w", err)
	}
	return report, nil
}

func limitsFromReport(report map[string]json.RawMessage) ([]contracts.Limit, error) {
	var found []contracts.Limit
	families := modelFamilies()
	for _, key := range slices.Sorted(maps.Keys(report)) {
		window, scope, ok := usageKeyWindow(key, families)
		if !ok {
			continue
		}
		var entry *usageWindow
		if err := json.Unmarshal(report[key], &entry); err != nil {
			return nil, fmt.Errorf("decode usage window %s: %w", key, err)
		}
		if entry == nil || entry.Utilization == nil {
			continue
		}
		found = append(found, newLimit(window, scope, *entry.Utilization, entry.ResetsAt))
	}
	scoped, err := scopedLimits(report["limits"])
	if err != nil {
		return nil, err
	}
	for _, limit := range scoped {
		if !hasLimit(found, limit.Window, limit.Scope) {
			found = append(found, limit)
		}
	}
	if len(found) == 0 {
		return nil, errUsageUnknown
	}
	slices.SortStableFunc(found, func(a, b contracts.Limit) int {
		return cmp.Or(cmp.Compare(a.Window, b.Window), cmp.Compare(a.Scope, b.Scope))
	})
	return found, nil
}

func usageKeyWindow(key string, families map[string]bool) (contracts.LimitWindow, string, bool) {
	switch key {
	case usageFiveHourKey:
		return contracts.LimitFiveHour, "", true
	case usageSevenDayKey:
		return contracts.LimitWeekly, "", true
	}
	family, scoped := strings.CutPrefix(key, usageSevenDayKey+"_")
	if !scoped || !families[family] {
		return 0, "", false
	}
	return contracts.LimitWeekly, family, true
}

func scopedLimits(raw json.RawMessage) ([]contracts.Limit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []usageLimit
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode usage limits: %w", err)
	}
	var found []contracts.Limit
	for _, entry := range entries {
		if entry.Group != usageWeeklyGroup || entry.Percent == nil || entry.Scope == nil || entry.Scope.Model == nil {
			continue
		}
		scope := strings.ToLower(strings.TrimSpace(entry.Scope.Model.DisplayName))
		if scope == "" {
			continue
		}
		found = append(found, newLimit(contracts.LimitWeekly, scope, *entry.Percent, entry.ResetsAt))
	}
	return found, nil
}

func newLimit(window contracts.LimitWindow, scope string, used float64, resetsAt string) contracts.Limit {
	limit := contracts.Limit{Window: window, Scope: scope, UsedPercent: min(max(used, 0), fullPercent), Span: windowSpan(window)}
	if at, err := time.Parse(time.RFC3339, resetsAt); err == nil {
		limit.ResetsAt = at
	}
	return limit
}

func windowSpan(window contracts.LimitWindow) time.Duration {
	switch window {
	case contracts.LimitFiveHour:
		return fiveHourSpan
	case contracts.LimitWeekly:
		return weekSpan
	case contracts.LimitMonthly:
	}
	return 0
}

func hasLimit(found []contracts.Limit, window contracts.LimitWindow, scope string) bool {
	return slices.ContainsFunc(found, func(limit contracts.Limit) bool {
		return limit.Window == window && limit.Scope == scope
	})
}

func modelFamilies() map[string]bool {
	families := make(map[string]bool)
	specs := Models()
	for i := range specs {
		spec := &specs[i]
		if spec.Route != contracts.RouteNative {
			continue
		}
		rest, isClaude := strings.CutPrefix(spec.ID, claudeIDPrefix)
		if family, _, _ := strings.Cut(rest, "-"); isClaude && family != "" {
			families[family] = true
		}
	}
	return families
}
