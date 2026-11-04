package claude

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const backendKind = "claude"

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:     backendKind,
			Category: contracts.CategoryBackend,
			Status:   contracts.StatusLive,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CLAUDE_CMD", Help: "base command to run the agent", Default: backendKind},
				{Key: "model", Env: "CLAUDE_MODEL", Help: "model override"},
				{Key: "stream", Env: "CLAUDE_STREAM", Help: "stream-json mode (false to disable)", Default: "true"},
				{Key: "dir", Env: "CLAUDE_DIR", Help: "working directory"},
				{Key: "kind", Env: "CLAUDE_KIND", Help: "backend kind"},
				{Key: "env", Env: "", Help: "per-session env injected at spawn (K=V per line); host-supplied, never from the environment"},
			},
			Models:       Models(),
			Capabilities: contracts.Capabilities{Gate: contracts.GrainTool, Compactor: true},
		},
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			return NewBackend(ctx, Config{
				Kind:     cfg.Get("kind"),
				Stream:   cfg.Get("stream") != "false",
				Cmd:      cfg.Get("cmd"),
				Model:    cfg.Get("model"),
				Dir:      cfg.Get("dir"),
				ResumeID: cfg.Get("resume"),
				Env:      contracts.ParseEnvSetting(cfg.Get("env")),
			})
		},
	})
}
