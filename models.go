package claude

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelOpus55       = "claude-opus-5-5"
	modelFable51      = "claude-fable-5-1"
	modelOpus5        = "claude-opus-5"
	modelFable5       = "claude-fable-5"
	modelSonnet5      = "claude-sonnet-5"
	modelSonnet55     = "claude-sonnet-5-5"
	modelHaiku55      = "claude-haiku-5-5"
	modelHaiku45Dated = "claude-haiku-4-5-20251001"
	modelOpus48       = "claude-opus-4-8"

	context1M   = 1_000_000
	context200K = 200_000

	opus55Input      = 4
	opus55Output     = 20
	opus55CacheRead  = 0.2
	opus55CacheWrite = 5

	fableInput       = 10
	fableOutput      = 50
	fableCacheWrite  = 12.5
	fable51CacheRead = 0.25
	fable5CacheRead  = 1

	opus5Input      = 5
	opus5Output     = 25
	opus5CacheRead  = 0.5
	opus5CacheWrite = 6.25

	sonnet5Input      = 2
	sonnet5Output     = 10
	sonnet5CacheRead  = 0.2
	sonnet5CacheWrite = 2.5
	sonnet55CacheRead = 0.1

	haiku55Input      = 0.1
	haiku55Output     = 0.5
	haiku55CacheRead  = 0.01
	haiku55CacheWrite = 0.125

	haiku45Input      = 1
	haiku45Output     = 5
	haiku45CacheRead  = 0.1
	haiku45CacheWrite = 1.25
)

// Models is the catalog of models this backend declares it can execute.
//
// The plugin is the Anthropic protocol runtime rather than a Claude-only backend: the same claude binary also runs other models that publish an Anthropic-compatible endpoint. Those entries arrive as gateway routes, without a single line of code change here. The name is kept because renaming it would migrate persisted state (state.Session.Vendor) across four repositories for a cosmetic gain.
//
// This catalog is the source of truth for the IDs and labels of the Anthropic models.
func Models() []contracts.ModelSpec {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	return []contracts.ModelSpec{
		{ID: modelOpus55, Label: "Opus 5.5", Arg: modelOpus55, Efforts: efforts, Route: contracts.RouteNative, InputPrice: opus55Input, OutputPrice: opus55Output, CacheReadPrice: opus55CacheRead, CacheWritePrice: opus55CacheWrite, ContextWindow: context1M},
		{ID: "claude-opus-5-5-1m", Label: "Opus 5.5 · 1M", Arg: "claude-opus-5-5[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: opus55Input, OutputPrice: opus55Output, CacheReadPrice: opus55CacheRead, CacheWritePrice: opus55CacheWrite, ContextWindow: context1M},
		{ID: modelFable51, Label: "Fable 5.1", Arg: modelFable51, Efforts: efforts, Route: contracts.RouteNative, InputPrice: fableInput, OutputPrice: fableOutput, CacheReadPrice: fable51CacheRead, CacheWritePrice: fableCacheWrite, ContextWindow: context1M},
		{ID: modelOpus5, Label: "Opus 5", Arg: modelOpus5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: opus5Input, OutputPrice: opus5Output, CacheReadPrice: opus5CacheRead, CacheWritePrice: opus5CacheWrite, ContextWindow: context1M, Hidden: true},
		{ID: modelFable5, Label: "Fable 5", Arg: modelFable5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: fableInput, OutputPrice: fableOutput, CacheReadPrice: fable5CacheRead, CacheWritePrice: fableCacheWrite, ContextWindow: context1M, Hidden: true},
		{ID: modelSonnet5, Label: "Sonnet 5", Arg: modelSonnet5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: sonnet5Input, OutputPrice: sonnet5Output, CacheReadPrice: sonnet5CacheRead, CacheWritePrice: sonnet5CacheWrite, ContextWindow: context1M, Hidden: true},
		{ID: modelSonnet55, Label: "Sonnet 5.5", Arg: modelSonnet55, Efforts: efforts, Route: contracts.RouteNative, InputPrice: sonnet5Input, OutputPrice: sonnet5Output, CacheReadPrice: sonnet55CacheRead, CacheWritePrice: sonnet5CacheWrite, ContextWindow: context1M},
		{ID: modelHaiku55, Label: "Haiku 5.5", Arg: modelHaiku55, Efforts: efforts, Route: contracts.RouteNative, InputPrice: haiku55Input, OutputPrice: haiku55Output, CacheReadPrice: haiku55CacheRead, CacheWritePrice: haiku55CacheWrite, ContextWindow: context1M},
		{ID: "claude-haiku-4-5", Label: "Haiku 4.5", Arg: modelHaiku45Dated, Efforts: efforts, Route: contracts.RouteNative, InputPrice: haiku45Input, OutputPrice: haiku45Output, CacheReadPrice: haiku45CacheRead, CacheWritePrice: haiku45CacheWrite, ContextWindow: context200K, Hidden: true},
		{ID: modelOpus48, Label: "Opus 4.8", Arg: modelOpus48, Efforts: efforts, Route: contracts.RouteNative, InputPrice: opus5Input, OutputPrice: opus5Output, CacheReadPrice: opus5CacheRead, CacheWritePrice: opus5CacheWrite, ContextWindow: context1M, Hidden: true},
		{ID: "claude-opus-4-8-1m", Label: "Opus 4.8 · 1M", Arg: "claude-opus-4-8[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: opus5Input, OutputPrice: opus5Output, CacheReadPrice: opus5CacheRead, CacheWritePrice: opus5CacheWrite, ContextWindow: context1M, Hidden: true},

		{ID: "gw-claude-opus-5-5", Label: "Opus 5.5", Arg: modelOpus55, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-fable-5-1", Label: "Fable 5.1", Arg: modelFable51, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-opus-5", Label: "Opus 5", Arg: modelOpus5, Efforts: efforts, Route: contracts.RouteGateway, Hidden: true},
		{ID: "gw-claude-sonnet-5", Label: "Sonnet 5", Arg: modelSonnet5, Efforts: efforts, Route: contracts.RouteGateway, Hidden: true},
		{ID: "gw-claude-sonnet-5-5", Label: "Sonnet 5.5", Arg: modelSonnet55, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-haiku-4-5", Label: "Haiku 4.5", Arg: modelHaiku45Dated, Efforts: efforts, Route: contracts.RouteGateway, Hidden: true},

		{ID: "gw-glm-4.7", Label: "GLM-4.7", Arg: "glm-4.7", Route: contracts.RouteGateway},
		{ID: "gw-qwen3-coder", Label: "Qwen3 Coder", Arg: "qwen3-coder", Route: contracts.RouteGateway},
		{ID: "gw-deepseek-v3", Label: "DeepSeek V3", Arg: "deepseek-v3", Route: contracts.RouteGateway},
	}
}
