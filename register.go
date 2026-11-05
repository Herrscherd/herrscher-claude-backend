package claude

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const backendKind = "claude"

func init() {
	texts := newMessages()
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:     backendKind,
			Category: contracts.CategoryBackend,
			Status:   contracts.StatusLive,
			Config: []contracts.Setting{
				{Key: "cmd", Env: "CLAUDE_CMD", Help: texts.english(keySettingCmd), Default: backendKind},
				{Key: "model", Env: "CLAUDE_MODEL", Help: texts.english(keySettingModel)},
				{Key: "stream", Env: "CLAUDE_STREAM", Help: texts.english(keySettingStream), Default: "true"},
				{Key: "dir", Env: "CLAUDE_DIR", Help: texts.english(keySettingDir)},
				{Key: "kind", Env: "CLAUDE_KIND", Help: texts.english(keySettingKind)},
				{Key: "env", Env: "", Help: texts.english(keySettingEnv)},
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
