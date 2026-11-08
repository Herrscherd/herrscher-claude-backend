package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	testOAuthToken = "placeholder-oauth-value"
	usageBody      = `{"five_hour":null,` +
		`"seven_day":{"utilization":47.0,"resets_at":"2026-11-13T14:00:00.267317+00:00"},` +
		`"seven_day_opus":{"utilization":12.5,"resets_at":"2026-11-13T14:00:00+00:00"},` +
		`"seven_day_sonnet":null,` +
		`"limits":[` +
		`{"kind":"weekly_all","group":"weekly","percent":47,"resets_at":"2026-11-13T14:00:00.267317+00:00","scope":null},` +
		`{"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-11-13T14:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"}}}` +
		`]}`
)

func testNow() time.Time {
	return time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
}

func writeCredentials(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"claudeAiOauth":{"accessToken":"` + testOAuthToken + `","expiresAt":` + strconv.FormatInt(expiresAt.UnixMilli(), 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, credentialsFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return dir
}

func configEnv(dir string) func(string) string {
	return func(name string) string {
		if name == contracts.EnvClaudeConfigDir {
			return dir
		}
		return ""
	}
}

func usageServer(t *testing.T, status int, body string, gotAuth, gotBeta *string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		*gotBeta = r.Header.Get(oauthBetaHeader)
		if r.URL.Path != usagePath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		mustWriteString(t, w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func findLimit(limits []contracts.Limit, window contracts.LimitWindow, scope string) (contracts.Limit, bool) {
	for _, limit := range limits {
		if limit.Window == window && limit.Scope == scope {
			return limit, true
		}
	}
	return contracts.Limit{}, false
}

func TestLimitsFromReadsUsageEndpoint(t *testing.T) {
	var gotAuth, gotBeta string
	server := usageServer(t, http.StatusOK, usageBody, &gotAuth, &gotBeta)
	dir := writeCredentials(t, testNow().Add(time.Hour))

	limits, err := limitsFrom(context.Background(), contracts.ProbeAccess{Getenv: configEnv(dir)}, newUsageClient(), server.URL, testNow())
	if err != nil {
		t.Fatalf("limitsFrom: %v", err)
	}
	if gotAuth != "Bearer "+testOAuthToken {
		t.Fatalf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotBeta != oauthBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBeta, oauthBeta)
	}
	if len(limits) != 3 {
		t.Fatalf("got %d limits, want 3: %+v", len(limits), limits)
	}
	weekly, ok := findLimit(limits, contracts.LimitWeekly, "")
	if !ok || weekly.UsedPercent != 47 || weekly.Span != weekSpan {
		t.Fatalf("whole account weekly = %+v, ok=%v", weekly, ok)
	}
	if want := time.Date(2026, time.November, 13, 14, 0, 0, 267317000, time.UTC); !weekly.ResetsAt.Equal(want) {
		t.Fatalf("weekly reset = %v, want %v", weekly.ResetsAt, want)
	}
	opus, ok := findLimit(limits, contracts.LimitWeekly, "opus")
	if !ok || opus.UsedPercent != 12.5 {
		t.Fatalf("opus weekly = %+v, ok=%v", opus, ok)
	}
	fable, ok := findLimit(limits, contracts.LimitWeekly, "fable")
	if !ok || fable.UsedPercent != 0 {
		t.Fatalf("fable weekly from the limits array = %+v, ok=%v", fable, ok)
	}
}

func TestLimitsFromReportMapsFiveHourWindow(t *testing.T) {
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"five_hour":{"utilization":30,"resets_at":"2026-11-08T16:00:00+00:00"}}`), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	limits, err := limitsFromReport(report)
	if err != nil {
		t.Fatalf("limitsFromReport: %v", err)
	}
	five, ok := findLimit(limits, contracts.LimitFiveHour, "")
	if !ok || five.UsedPercent != 30 || five.Span != fiveHourSpan {
		t.Fatalf("five hour = %+v, ok=%v", five, ok)
	}
}

func TestLimitsFromFailures(t *testing.T) {
	tests := []struct {
		name            string
		missingFile     bool
		expiresAt       time.Time
		status          int
		wantUnavailable bool
		wantCause       error
	}{
		{
			name:            "no credentials file",
			missingFile:     true,
			status:          http.StatusOK,
			wantUnavailable: true,
			wantCause:       errNoOAuthLogin,
		},
		{
			name:            "expired token",
			expiresAt:       testNow().Add(-time.Minute),
			status:          http.StatusOK,
			wantUnavailable: true,
			wantCause:       errOAuthExpired,
		},
		{
			name:            "token refused",
			expiresAt:       testNow().Add(time.Hour),
			status:          http.StatusUnauthorized,
			wantUnavailable: true,
			wantCause:       errUsageRefused,
		},
		{
			name:            "server error",
			expiresAt:       testNow().Add(time.Hour),
			status:          http.StatusInternalServerError,
			wantUnavailable: false,
			wantCause:       errUsageRefused,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth, gotBeta string
			server := usageServer(t, tt.status, `{}`, &gotAuth, &gotBeta)
			dir := t.TempDir()
			if !tt.missingFile {
				dir = writeCredentials(t, tt.expiresAt)
			}
			_, err := limitsFrom(context.Background(), contracts.ProbeAccess{Getenv: configEnv(dir)}, newUsageClient(), server.URL, testNow())
			assertUsageFailure(t, err, tt.wantUnavailable, tt.wantCause)
		})
	}
}

func assertUsageFailure(t *testing.T, err error, wantUnavailable bool, wantCause error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if got := errors.Is(err, contracts.ErrLimitsUnavailable); got != wantUnavailable {
		t.Fatalf("ErrLimitsUnavailable match = %v, want %v: %v", got, wantUnavailable, err)
	}
	if !errors.Is(err, wantCause) {
		t.Fatalf("error %v does not carry its cause %v", err, wantCause)
	}
	if strings.Contains(err.Error(), testOAuthToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
}
