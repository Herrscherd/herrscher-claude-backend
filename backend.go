package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Akayashuu/herrscher-contracts"
)

// Config configures a Claude backend. Kind selects the response strategy:
//
//	"stream"  — one persistent claude stream-json process (default)
//	"oneshot" — run Cmd fresh per message
//
// When Kind is empty it is resolved from Stream (the legacy toggle): true →
// stream, false → oneshot.
type Config struct {
	Kind    string // "stream"|"oneshot"; "" resolves from Stream
	Stream  bool   // legacy toggle used only when Kind == ""
	Cmd     string // base command (split on whitespace)
	Model   string // --model value (stream)
	Dir     string // working dir ("" = cwd)
	Verbose bool   // reserved for backend diagnostics on stderr
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
		cmdStr := c.Cmd
		return &oneShotResponder{run: func(ctx context.Context, p contracts.Prompt) (string, error) {
			return runCmd(ctx, cmdStr, p)
		}}, nil
	default: // "stream"
		r := &streamResponder{ctx: ctx, base: streamBase(strings.Fields(c.Cmd)), model: c.Model}
		r.dir = c.Dir
		return r, nil
	}
}

// runCmd executes cmdStr (split on whitespace) with the message text appended
// as the final argument, piped on stdin, and exposed via DCTL_* env vars.
// DCTL_ATTACHMENTS lists local image paths joined by the OS path-list separator
// (':' on Unix), the same convention as $PATH, so a consumer splits it the way
// it would split $PATH.
func runCmd(ctx context.Context, cmdStr string, p contracts.Prompt) (string, error) {
	fields := strings.Fields(cmdStr)
	args := append(fields[1:], p.Content)
	cmd := exec.CommandContext(ctx, fields[0], args...)
	cmd.Stdin = strings.NewReader(p.Content)
	cmd.Env = append(os.Environ(),
		"DCTL_MSG="+p.Content,
		"DCTL_AUTHOR="+p.Author,
		"DCTL_MESSAGE_ID="+p.MessageID,
		"DCTL_CHANNEL="+p.ChannelID,
		"DCTL_ATTACHMENTS="+strings.Join(p.Attachments, string(os.PathListSeparator)),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// modelPresets are the models offered as ready-made cmd choices, friendly label
// → claude --model value. The [1m] suffix selects the 1M context window (absence
// = standard 200k). Keep the highest-context/priciest entries clearly labelled.
var modelPresets = []struct{ label, model string }{
	{"Opus 4.8 · 200k", "claude-opus-4-8"},
	{"Opus 4.8 · 1M", "claude-opus-4-8[1m]"},
	{"Sonnet 4.6", "claude-sonnet-4-6"},
	{"Haiku 4.5", "claude-haiku-4-5-20251001"},
}

// effortPresets are claude's reasoning-effort levels, cheapest → priciest.
var effortPresets = []string{"low", "medium", "high", "xhigh", "max"}

// CommandPresets returns the ready-made /session cmd choices (the model × effort
// matrix) targeting binary bin, as label→command, for the host's autocomplete.
// It does NOT include the "Default" entry (the host prepends that).
func CommandPresets(bin string) []contracts.Choice {
	out := make([]contracts.Choice, 0, len(modelPresets)*len(effortPresets))
	for _, m := range modelPresets {
		for _, e := range effortPresets {
			out = append(out, contracts.Choice{
				Label: m.label + " · " + e,
				Value: bin + " --model " + m.model + " --effort " + e,
			})
		}
	}
	return out
}
