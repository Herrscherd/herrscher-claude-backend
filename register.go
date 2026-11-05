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
				{Key: "cmd", Env: "CLAUDE_CMD", Help: englishText(keySettingCmd), Default: backendKind},
				{Key: "model", Env: "CLAUDE_MODEL", Help: englishText(keySettingModel)},
				{Key: "stream", Env: "CLAUDE_STREAM", Help: englishText(keySettingStream), Default: "true"},
				{Key: "dir", Env: "CLAUDE_DIR", Help: englishText(keySettingDir)},
				{Key: "kind", Env: "CLAUDE_KIND", Help: englishText(keySettingKind)},
				{Key: "env", Env: "", Help: englishText(keySettingEnv)},
			},
			Models:       Models(),
			Capabilities: contracts.Capabilities{Gate: contracts.GrainTool, Compactor: true},
		},
		Discover: discover,
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
