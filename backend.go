package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Herrscherd/herrscher-contracts"
)

// Config configures a Claude backend. Kind selects the response strategy:
//
//	"stream"  — one persistent claude stream-json process (default)
//	"oneshot" — run Cmd fresh per message
//
// When Kind is empty it is resolved from Stream (the legacy toggle): true →
// stream, false → oneshot.
type Config struct {
	Kind   string // "stream"|"oneshot"; "" resolves from Stream
	Stream bool   // legacy toggle used only when Kind == ""
	Cmd    string // base command (split on whitespace)
	Model  string // --model value (stream)
	Dir    string // working dir ("" = cwd)

	ResumeID string // claude session id to resume on first start ("" = fresh)

	// Env is injected into the child process's environment at every spawn. It
	// carries gateway credentials and is NEVER persisted or logged: that is
	// what distinguishes it from Cmd, which ends up in state.json and in `ps`.
	Env map[string]string
}

// resolveBackend picks the backend kind. An explicit kind always wins. When
// unset, the default is stream (persistent claude stream-json); stream is the
// legacy toggle and only consulted here, where false selects the one-shot kind.
func resolveBackend(kind string, stream bool) string {
	if kind != "" {
		return kind
	}
	if stream {
		return "stream"
	}
	return "oneshot"
}

// NewBackend builds the configured backend. It resolves the kind (from
// Kind/Stream) and returns an error if the oneshot kind has an empty Cmd.
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error) {
	switch resolveBackend(c.Kind, c.Stream) {
	case "oneshot":
		if strings.TrimSpace(c.Cmd) == "" {
			return nil, fmt.Errorf("oneshot backend requires a non-empty Cmd")
		}
		cmdStr, env := c.Cmd, c.Env
		return &oneShotResponder{run: func(ctx context.Context, p contracts.Prompt) (string, error) {
			return runCmd(ctx, cmdStr, env, p)
		}}, nil
	default: // "stream"
		r := &streamResponder{ctx: ctx, base: streamBase(strings.Fields(c.Cmd)), model: c.Model, resumeID: c.ResumeID, env: c.Env}
		r.dir = c.Dir
		return r, nil
	}
}

// runCmd executes cmdStr (split on whitespace) with the message text appended
// as the final argument and piped on stdin. env is merged over the daemon's
// inherited environment (see contracts.MergeEnv): with no injection, the child
// process environment is unchanged.
func runCmd(ctx context.Context, cmdStr string, env map[string]string, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	content := withContext(p.Context, withAttachments(p.Content, p.Attachments))
	args := append(fields[1:], content)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = contracts.MergeEnv(os.Environ(), env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("oneshot backend %q: %w", fields[0], err)
	}
	return string(out), nil
}
