package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func fakeClaude(t *testing.T, authJSON, turnJSON string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = auth ]; then\n" +
		"  echo '" + authJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' \"$@\" > '" + fakeArgsFile(binary) + "'\n" +
		"echo '" + turnJSON + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return binary
}

func fakeArgsFile(binary string) string {
	return filepath.Join(filepath.Dir(binary), "args")
}

func probeAccess(binary string, account contracts.ProfileName, deep bool) contracts.ProbeAccess {
	return contracts.ProbeAccess{
		Getenv:  os.Getenv,
		Account: account,
		Config:  contracts.PluginConfig{Settings: map[string]string{"cmd": binary}},
		Deep:    deep,
	}
}

func TestProbeNotLoggedInIsOffWithRemedy(t *testing.T) {
	tests := []struct {
		name    string
		account contracts.ProfileName
		command string
	}{
		{name: "native", account: "", command: "claude auth login"},
		{name: "profile", account: "work", command: "herrscher account add claude work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary := fakeClaude(t, `{"loggedIn": false}`, `{}`)
			report := probe(context.Background(), probeAccess(binary, tt.account, false))
			if report.State != contracts.HealthOff {
				t.Fatalf("state = %v, want off", report.State)
			}
			if report.Detail.Code != contracts.CodeHealthNotLoggedIn {
				t.Fatalf("detail = %v, want not logged in", report.Detail.Code)
			}
			if report.Remedy.Code != contracts.CodeHealthRemedyRun || len(report.Remedy.Args) != 1 {
				t.Fatalf("remedy = %+v, want a run command", report.Remedy)
			}
			if got := report.Remedy.Args[0].Value(); got != tt.command {
				t.Fatalf("remedy command = %v, want %q", got, tt.command)
			}
		})
	}
}

func TestProbeLoggedInIsOKWithoutDeepTurn(t *testing.T) {
	binary := fakeClaude(t, `{"loggedIn": true}`, `{}`)
	report := probe(context.Background(), probeAccess(binary, "", false))
	if report.State != contracts.HealthOK {
		t.Fatalf("state = %v, want ok", report.State)
	}
	if report.Detail.Code != contracts.CodeHealthConnected {
		t.Fatalf("detail = %v, want connected", report.Detail.Code)
	}
	if _, err := os.Stat(fakeArgsFile(binary)); err == nil {
		t.Fatal("a cheap probe must not run a model turn")
	}
}

func TestProbeDeepReportsTurnOutcome(t *testing.T) {
	tests := []struct {
		name   string
		turn   string
		state  contracts.HealthState
		detail contracts.Code
	}{
		{name: "answered", turn: `{"is_error":false,"result":"ok"}`, state: contracts.HealthOK, detail: contracts.CodeHealthConnected},
		{name: "rate limited", turn: `{"is_error":true,"api_error_status":429}`, state: contracts.HealthDegraded, detail: contracts.CodeHealthRateLimited},
		{name: "usage limit", turn: `{"is_error":true,"api_error":"usage_limit_reached"}`, state: contracts.HealthDegraded, detail: contracts.CodeHealthRateLimited},
		{name: "refused", turn: `{"is_error":true,"api_error_status":401}`, state: contracts.HealthDown, detail: contracts.CodeHealthCredentialsRefused},
		{name: "server error", turn: `{"is_error":true,"api_error_status":503}`, state: contracts.HealthDegraded, detail: contracts.CodeHealthUnreachable},
		{name: "client error", turn: `{"is_error":true,"api_error_status":404}`, state: contracts.HealthDown, detail: contracts.CodeHealthUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary := fakeClaude(t, `{"loggedIn": true}`, tt.turn)
			report := probe(context.Background(), probeAccess(binary, "", true))
			if report.State != tt.state {
				t.Fatalf("state = %v, want %v", report.State, tt.state)
			}
			if report.Detail.Code != tt.detail {
				t.Fatalf("detail = %v, want %v", report.Detail.Code, tt.detail)
			}
		})
	}
}

func TestProbeDeepRunsCheapestNativeModelWithoutTools(t *testing.T) {
	binary := fakeClaude(t, `{"loggedIn": true}`, `{"is_error":false,"result":"ok"}`)
	probe(context.Background(), probeAccess(binary, "", true))
	raw, err := os.ReadFile(fakeArgsFile(binary))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(raw)
	if want := "\n--model\nclaude-haiku-5-5\n"; !strings.Contains("\n"+args, want) {
		t.Fatalf("args %q lack %q", args, want)
	}
	if want := "\n--tools\n\n"; !strings.Contains("\n"+args, want) {
		t.Fatalf("args %q do not pass an empty --tools", args)
	}
}

func TestCheapestModelIsTheLowestPricedVisibleNativeModel(t *testing.T) {
	cheapest := cheapestModel()
	if cheapest != "claude-haiku-5-5" {
		t.Fatalf("cheapestModel() = %q, want claude-haiku-5-5", cheapest)
	}
	specs := Models()
	price := func(arg string) float64 {
		for _, spec := range specs {
			if spec.Arg == arg {
				return spec.InputPrice + spec.OutputPrice
			}
		}
		t.Fatalf("%q is not in the catalogue", arg)
		return 0
	}
	for _, spec := range specs {
		if spec.Route != contracts.RouteNative || spec.Hidden {
			continue
		}
		if price(spec.Arg) < price(cheapest) {
			t.Fatalf("%s is cheaper than the cheapest %q", spec.Arg, cheapest)
		}
	}
}

func fakeSleepingClaude(t *testing.T, authJSON string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = auth ]; then\n" +
		"  echo '" + authJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"touch '" + fakeTurnMarker(binary) + "'\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return binary
}

func fakeTurnMarker(binary string) string {
	return filepath.Join(filepath.Dir(binary), "turn-started")
}

func TestProbeDeepTurnPastDeadlineIsDegradedTimeout(t *testing.T) {
	binary := fakeSleepingClaude(t, `{"loggedIn": true}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	report := probe(ctx, probeAccess(binary, "", true))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("probe took %v, want it to return once the deadline passes", elapsed)
	}
	if _, err := os.Stat(fakeTurnMarker(binary)); err != nil {
		t.Fatalf("the deadline passed before the turn started: %v", err)
	}
	if report.State != contracts.HealthDegraded {
		t.Fatalf("state = %v, want degraded", report.State)
	}
	if report.Detail.Code != contracts.CodeHealthUnreachable || len(report.Detail.Args) != 1 {
		t.Fatalf("detail = %+v, want unreachable with a reason", report.Detail)
	}
	if got := report.Detail.Args[0].Value(); got != reasonTimeout {
		t.Fatalf("reason = %v, want %q", got, reasonTimeout)
	}
}

func TestProbeMissingBinaryIsDown(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "absent")
	report := probe(context.Background(), probeAccess(binary, "", false))
	if report.State != contracts.HealthDown {
		t.Fatalf("state = %v, want down", report.State)
	}
	if report.Detail.Code != contracts.CodeHealthUnreachable {
		t.Fatalf("detail = %v, want unreachable", report.Detail.Code)
	}
}
