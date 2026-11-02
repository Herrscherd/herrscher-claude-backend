package claude

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelOpus55       = "claude-opus-5-5"
	modelFable51      = "claude-fable-5-1"
	modelOpus5        = "claude-opus-5"
	modelFable5       = "claude-fable-5"
	modelSonnet5      = "claude-sonnet-5"
	modelHaiku45Dated = "claude-haiku-4-5-20251001"

	opusInputPrice   = 5
	fableInputPrice  = 10
	sonnetInputPrice = 3
	haikuInputPrice  = 1
)

// Models is the catalog of models this backend declares it can execute.
//
// The plugin is the Anthropic protocol runtime rather than a Claude-only backend: the same claude binary also runs other models that publish an Anthropic-compatible endpoint. Those entries arrive as gateway routes, without a single line of code change here. The name is kept because renaming it would migrate persisted state (state.Session.Vendor) across four repositories for a cosmetic gain.
//
// This catalog is the source of truth for the IDs and labels of the Anthropic models.
func Models() []contracts.ModelSpec {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	return []contracts.ModelSpec{
		{ID: modelOpus55, Label: "Opus 5.5", Arg: modelOpus55, Efforts: efforts, Route: contracts.RouteNative, InputPrice: opusInputPrice},
		{ID: "claude-opus-5-5-1m", Label: "Opus 5.5 · 1M", Arg: "claude-opus-5-5[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: opusInputPrice},
		{ID: modelFable51, Label: "Fable 5.1", Arg: modelFable51, Efforts: efforts, Route: contracts.RouteNative, InputPrice: fableInputPrice},
		{ID: modelOpus5, Label: "Opus 5", Arg: modelOpus5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: opusInputPrice},
		{ID: modelFable5, Label: "Fable 5", Arg: modelFable5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: fableInputPrice},
		{ID: modelSonnet5, Label: "Sonnet 5", Arg: modelSonnet5, Efforts: efforts, Route: contracts.RouteNative, InputPrice: sonnetInputPrice},
		{ID: "claude-haiku-4-5", Label: "Haiku 4.5", Arg: modelHaiku45Dated, Efforts: efforts, Route: contracts.RouteNative, InputPrice: haikuInputPrice},
		{ID: "claude-opus-4-8", Label: "Opus 4.8", Arg: "claude-opus-4-8", Efforts: efforts, Route: contracts.RouteNative, InputPrice: opusInputPrice},
		{ID: "claude-opus-4-8-1m", Label: "Opus 4.8 · 1M", Arg: "claude-opus-4-8[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: opusInputPrice},

		{ID: "gw-claude-opus-5-5", Label: "Opus 5.5", Arg: modelOpus55, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-fable-5-1", Label: "Fable 5.1", Arg: modelFable51, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-opus-5", Label: "Opus 5", Arg: modelOpus5, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-sonnet-5", Label: "Sonnet 5", Arg: modelSonnet5, Efforts: efforts, Route: contracts.RouteGateway},
		{ID: "gw-claude-haiku-4-5", Label: "Haiku 4.5", Arg: modelHaiku45Dated, Efforts: efforts, Route: contracts.RouteGateway},

		{ID: "gw-glm-4.7", Label: "GLM-4.7", Arg: "glm-4.7", Route: contracts.RouteGateway},
		{ID: "gw-qwen3-coder", Label: "Qwen3 Coder", Arg: "qwen3-coder", Route: contracts.RouteGateway},
		{ID: "gw-deepseek-v3", Label: "DeepSeek V3", Arg: "deepseek-v3", Route: contracts.RouteGateway},
	}
}
