package claude

import (
	"context"

	"github.com/Herrscherd/herrscher-contracts"
)

// init self-registers the Claude backend into the global plugin registry. A
// blank import of this package (in the host's generated plugins.go) makes the
// backend discoverable with no host wiring. The factory maps the neutral
// PluginConfig onto the backend's Config.
func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:     "claude",
			Category: contracts.CategoryBackend,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CLAUDE_CMD", Help: "base command to run the agent", Default: "claude"},
				{Key: "model", Env: "CLAUDE_MODEL", Help: "model override"},
				{Key: "stream", Env: "CLAUDE_STREAM", Help: "stream-json mode (false to disable)", Default: "true"},
				{Key: "dir", Env: "CLAUDE_DIR", Help: "working directory"},
				{Key: "kind", Env: "CLAUDE_KIND", Help: "backend kind"},
			},
		},
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			return NewBackend(ctx, Config{
				Kind:     cfg.Get("kind"),
				Stream:   cfg.Get("stream") != "false",
				Cmd:      cfg.Get("cmd"),
				Model:    cfg.Get("model"),
				Dir:      cfg.Get("dir"),
				ResumeID: cfg.Get("resume"),
			})
		},
	})
}
