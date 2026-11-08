package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	nativeLoginCommand  = "claude auth login"
	profileLoginCommand = "herrscher account add claude "
	probePrompt         = "Reply with the single word ok."
	probeWaitDelay      = time.Second
	reasonTimeout       = "timeout"
	reasonCLINotFound   = "claude cli not found"
	reasonUnreadable    = "unreadable claude output"
	reasonTurnFailed    = "turn failed"
	reasonHTTPPrefix    = "http "
	usageLimitReached   = "usage_limit_reached"
)

func probeEnvNames() []string {
	return []string{
		"HOME",
		"PATH",
		contracts.EnvClaudeConfigDir,
		contracts.EnvAnthropicAPIKey,
		contracts.EnvAnthropicBaseURL,
		legacyAnthropicAuthVar,
		"CLAUDE_CODE_OAUTH_TOKEN",
	}
}

type authStatus struct {
	LoggedIn bool `json:"loggedIn"`
}

type probeTurn struct {
	IsError        bool   `json:"is_error"`
	APIErrorStatus int    `json:"api_error_status"`
	APIError       string `json:"api_error"`
}

func probe(ctx context.Context, access contracts.ProbeAccess) contracts.ProbeReport {
	started := time.Now()
	binary := probeBinary(access.Config)
	env := probeEnv(probeGetenv(access))
	var status authStatus
	if report, ok := runProbeCommand(ctx, env, &status, binary, "auth", "status"); !ok {
		return report
	}
	if !status.LoggedIn {
		return contracts.ProbeReport{
			State:  contracts.HealthOff,
			Detail: contracts.Notice{Code: contracts.CodeHealthNotLoggedIn},
			Remedy: loginRemedy(access.Account),
		}
	}
	if access.Deep {
		var turn probeTurn
		if report, ok := runProbeCommand(ctx, env, &turn, binary, deepProbeArgs()...); !ok {
			return report
		}
		if turn.IsError {
			return failedTurnReport(turn, access.Account)
		}
	}
	return contracts.ProbeReport{State: contracts.HealthOK, Latency: time.Since(started), Detail: contracts.Notice{Code: contracts.CodeHealthConnected}}
}

func probeBinary(config contracts.PluginConfig) string {
	return streamBase(strings.Fields(config.Get("cmd")))[0]
}

func probeArgv(binary string, args ...string) []string {
	return append([]string{binary}, args...)
}

func probeEnv(getenv func(string) string) []string {
	names := probeEnvNames()
	base := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(names, name)
	})
	overrides := make(map[string]string, len(names))
	for _, name := range names {
		if value := getenv(name); value != "" {
			overrides[name] = value
		}
	}
	return spawnEnv(base, overrides)
}

func loginRemedy(account contracts.ProfileName) contracts.Notice {
	command := nativeLoginCommand
	if account != "" {
		command = profileLoginCommand + string(account)
	}
	return contracts.Notice{Code: contracts.CodeHealthRemedyRun, Args: []contracts.Arg{contracts.Text("Command", command)}}
}

func deepProbeArgs() []string {
	return []string{
		"-p", probePrompt,
		"--output-format", "json",
		"--model", cheapestModel(),
		"--tools", "",
		"--strict-mcp-config",
		"--no-session-persistence",
	}
}

func cheapestModel() string {
	var cheapest contracts.ModelSpec
	specs := Models()
	for i := range specs {
		spec := &specs[i]
		if spec.Route != contracts.RouteNative || spec.Hidden {
			continue
		}
		if cheapest.Arg == "" || spec.InputPrice+spec.OutputPrice < cheapest.InputPrice+cheapest.OutputPrice {
			cheapest = *spec
		}
	}
	return cheapest.Arg
}

func runProbeCommand(ctx context.Context, env []string, into any, binary string, args ...string) (contracts.ProbeReport, bool) {
	argv := probeArgv(binary, args...)
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = env
	command.WaitDelay = probeWaitDelay
	out, runErr := command.Output()
	if ctx.Err() != nil {
		return unreachable(contracts.HealthDegraded, reasonTimeout), false
	}
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist) {
		return unreachable(contracts.HealthDown, reasonCLINotFound), false
	}
	if err := json.Unmarshal(out, into); err != nil {
		return unreachable(contracts.HealthDown, reasonUnreadable), false
	}
	return contracts.ProbeReport{}, true
}

func failedTurnReport(turn probeTurn, account contracts.ProfileName) contracts.ProbeReport {
	switch {
	case turn.APIErrorStatus == http.StatusTooManyRequests || turn.APIError == usageLimitReached:
		return contracts.ProbeReport{State: contracts.HealthDegraded, Detail: contracts.Notice{Code: contracts.CodeHealthRateLimited}}
	case turn.APIErrorStatus == http.StatusUnauthorized || turn.APIErrorStatus == http.StatusForbidden:
		return contracts.ProbeReport{State: contracts.HealthDown, Detail: contracts.Notice{Code: contracts.CodeHealthCredentialsRefused}, Remedy: loginRemedy(account)}
	case turn.APIErrorStatus >= http.StatusInternalServerError:
		return unreachable(contracts.HealthDegraded, reasonHTTPPrefix+strconv.Itoa(turn.APIErrorStatus))
	case turn.APIErrorStatus > 0:
		return unreachable(contracts.HealthDown, reasonHTTPPrefix+strconv.Itoa(turn.APIErrorStatus))
	}
	return unreachable(contracts.HealthDown, reasonTurnFailed)
}

func unreachable(state contracts.HealthState, reason string) contracts.ProbeReport {
	return contracts.ProbeReport{State: state, Detail: contracts.Notice{Code: contracts.CodeHealthUnreachable, Args: []contracts.Arg{contracts.Text("Reason", reason)}}}
}
