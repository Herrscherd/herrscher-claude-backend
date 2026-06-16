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
		},
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			return NewBackend(ctx, Config{
				Kind:   cfg.Get("kind"),
				Stream: cfg.Get("stream") != "false",
				Cmd:    cfg.Get("cmd"),
				Model:  cfg.Get("model"),
				Dir:    cfg.Get("dir"),
			})
		},
	})
}
