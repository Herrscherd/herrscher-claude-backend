package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var (
	ErrGatewayPairIncomplete = errors.New("refusing to spawn: the session would run on the machine's own subscription while being routed through the gateway")
	ErrOneshotNeedsCmd       = errors.New("oneshot backend requires a non-empty Cmd")
)

// Config configures a Claude backend. Kind selects the response strategy:
//
//	"stream": one persistent claude stream-json process (default)
//	"oneshot": run Cmd fresh per message
//
// When Kind is empty it is resolved from Stream (the legacy toggle): true →
// stream, false → oneshot.
type Config struct {
	Kind   string
	Stream bool
	Cmd    string
	Model  string
	Dir    string

	ResumeID string

	// Env is injected into the child process's environment at every spawn. It
	// carries gateway credentials and is NEVER persisted or logged: that is
	// what distinguishes it from Cmd, which ends up in state.json and in `ps`.
	Env map[string]string
}

func resolveBackend(kind string, stream bool) string {
	if kind != "" {
		return kind
	}
	if stream {
		return "stream"
	}
	return "oneshot"
}

func checkGatewayPair(env map[string]string) error {
	if env[contracts.EnvAnthropicBaseURL] == "" {
		return nil
	}
	if strings.TrimSpace(env[contracts.EnvAnthropicAPIKey]) == "" {
		return fmt.Errorf("%w: %s is set without %s", ErrGatewayPairIncomplete, contracts.EnvAnthropicBaseURL, contracts.EnvAnthropicAPIKey)
	}
	return nil
}

const legacyAnthropicAuthToken = "ANTHROPIC_AUTH_TOKEN"

func spawnEnv(base []string, env map[string]string) []string {
	merged := contracts.MergeEnv(base, env)
	if env[contracts.EnvAnthropicBaseURL] == "" {
		return merged
	}
	scrubbed := make([]string, 0, len(merged))
	for _, kv := range merged {
		if strings.HasPrefix(kv, legacyAnthropicAuthToken+"=") {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	return scrubbed
}

// NewBackend builds the configured backend. It resolves the kind (from
// Kind/Stream) and returns an error if the oneshot kind has an empty Cmd, or
// if the injected environment carries a gateway base URL with no token.
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error) {
	if err := checkGatewayPair(c.Env); err != nil {
		return nil, err
	}
	switch resolveBackend(c.Kind, c.Stream) {
	case "oneshot":
		if strings.TrimSpace(c.Cmd) == "" {
			return nil, ErrOneshotNeedsCmd
		}
		cmdStr, env := c.Cmd, c.Env
		return &oneShotResponder{run: func(ctx context.Context, p contracts.Prompt) (string, error) {
			return runCmd(ctx, cmdStr, env, p)
		}}, nil
	default:
		return newStreamResponder(ctx, streamBase(strings.Fields(c.Cmd)), c.Model, c.ResumeID, c.Dir, c.Env), nil
	}
}

func runCmd(ctx context.Context, cmdStr string, env map[string]string, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	content := withContext(p.Context, withAttachments(p.Content, p.Attachments))
	args := append(fields[1:], content)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = spawnEnv(os.Environ(), env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("oneshot backend %q: %w", fields[0], err)
	}
	return string(out), nil
}
