package claude

import contracts "github.com/Herrscherd/herrscher-contracts"

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Models is the catalog this backend declares it knows how to execute.
//
// Despite its name, this plugin is not "the Claude backend" but the Anthropic
// protocol runtime: the same claude binary also runs GLM, Qwen, and DeepSeek,
// all of which publish an Anthropic-compatible endpoint. Those entries arrive as
// gateway routes, without a single line of code change here. The name is kept
// because renaming it would migrate persisted state (state.Session.Vendor)
// across four repositories for a cosmetic gain.
//
// IDs and labels are drawn from MODEL_CATALOG.claude in the app, which becomes
// obsolete once this source takes over.
var Models = []contracts.ModelSpec{
	{ID: "claude-opus-5-5", Label: "Opus 5.5", Arg: "claude-opus-5-5", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 5},
	{ID: "claude-opus-5-5-1m", Label: "Opus 5.5 · 1M", Arg: "claude-opus-5-5[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 5},
	{ID: "claude-fable-5-1", Label: "Fable 5.1", Arg: "claude-fable-5-1", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 10},
	{ID: "claude-opus-5", Label: "Opus 5", Arg: "claude-opus-5", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 5},
	{ID: "claude-fable-5", Label: "Fable 5", Arg: "claude-fable-5", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 10},
	{ID: "claude-sonnet-5", Label: "Sonnet 5", Arg: "claude-sonnet-5", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 3},
	{ID: "claude-haiku-4-5", Label: "Haiku 4.5", Arg: "claude-haiku-4-5-20251001", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 1},
	{ID: "claude-opus-4-8", Label: "Opus 4.8", Arg: "claude-opus-4-8", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 5},
	{ID: "claude-opus-4-8-1m", Label: "Opus 4.8 · 1M", Arg: "claude-opus-4-8[1m]", Efforts: efforts, Route: contracts.RouteNative, InputPrice: 5},

	{ID: "gw-claude-opus-5-5", Label: "Opus 5.5", Arg: "claude-opus-5-5", Efforts: efforts, Route: contracts.RouteGateway},
	{ID: "gw-claude-fable-5-1", Label: "Fable 5.1", Arg: "claude-fable-5-1", Efforts: efforts, Route: contracts.RouteGateway},
	{ID: "gw-claude-opus-5", Label: "Opus 5", Arg: "claude-opus-5", Efforts: efforts, Route: contracts.RouteGateway},
	{ID: "gw-claude-sonnet-5", Label: "Sonnet 5", Arg: "claude-sonnet-5", Efforts: efforts, Route: contracts.RouteGateway},
	{ID: "gw-claude-haiku-4-5", Label: "Haiku 4.5", Arg: "claude-haiku-4-5-20251001", Efforts: efforts, Route: contracts.RouteGateway},

	{ID: "gw-glm-4.7", Label: "GLM-4.7", Arg: "glm-4.7", Route: contracts.RouteGateway},
	{ID: "gw-qwen3-coder", Label: "Qwen3 Coder", Arg: "qwen3-coder", Route: contracts.RouteGateway},
	{ID: "gw-deepseek-v3", Label: "DeepSeek V3", Arg: "deepseek-v3", Route: contracts.RouteGateway},
}
