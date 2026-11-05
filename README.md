# herrscher-claude-backend

**The model edge for Claude.** It turns one neutral `contracts.Prompt` into a
reply, streaming intermediate progress events (text, thinking, tool calls, token
usage, cost) as they arrive.

It drives the local `claude` CLI and maps its stream-json output onto backend
events. It is a library rather than a binary, and it is the only module in the
platform that knows Claude exists.

Category: backend, the model edge. Ports: `Backend`, `ResumeAware`,
`SkillNative`. Status: live.

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-claude-backend
```

## Configuration

| Setting | Default | What it is |
|---|---|---|
| `CLAUDE_CMD` | `claude` | the binary to run |
| `CLAUDE_MODEL` | | the model a session gets when it names none |
| `CLAUDE_STREAM` | `true` | `false` selects the oneshot strategy |
| `CLAUDE_DIR` | | the working directory the CLI runs in |
| `CLAUDE_KIND` | | `stream` or `oneshot`, the explicit form of `CLAUDE_STREAM` |

There is one more setting, `env`, and it has no environment binding on purpose.
The host injects it per session, `K=V` per line, merged onto every spawned child.
It is never read from the daemon's own environment.

## Two strategies

`stream`, the default, keeps one persistent `claude` process alive per session
and speaks stream-json over stdin and stdout, so context, tools and cost
accumulate across turns. If the process dies mid-turn it emits `{Kind:"reset"}`,
restarts with `--resume <session id>`, and retries once.

`oneshot` runs `CLAUDE_CMD` fresh for every message, with the content appended as
the final argument and piped on stdin. It requires a non-empty command.

## Resume and memory

The current claude session id is exposed through `ResumeToken()`. The host
persists it and feeds it back at construction as the `resume` setting. That key
is read from `PluginConfig` but is deliberately not a user-facing manifest
setting, and not an environment variable.

`Prompt.Context`, which is the memory recall, is prepended inside a
`<memory data-only="true">` fence. Any `<memory>` tag the recalled text carries
is neutralized first, so it cannot forge or close the fence.

## Model catalog

`Models` is the declared catalog, published through `Manifest.Models`. Each entry
carries its id, label, efforts and its `Route`: `native` uses the machine's own
vendor login, `gateway` uses the product's account. `Arg` is what goes on the
CLI's `--model`, and `ID` is what the host persists.

Each native entry carries its price in USD per million tokens (input, output,
cache read, and the 5-minute cache write) and its context window in tokens. The
values are the base tier of the Anthropic pricing page
(https://platform.claude.com/docs/en/about-claude/pricing) and the model pages of
the Anthropic docs. Haiku 4.5 is 200K tokens, every other model listed is 1M.

What the catalog does not model:

- The 1-hour cache write. Only the 5-minute write goes in `CacheWritePrice`.
- The Claude Haiku 5.5 long-prompt tier. A prompt over 100,000 tokens costs $0.50
  input, $0.625 5-minute cache write, $1 1-hour cache write, $0.05 cache read and
  $2.50 output. The contract has no tier field, so the entry carries the base
  tier only, and a request over 100,000 tokens is under-priced by a factor of five.
- The batch discount (50%) and the US-only inference multiplier (1.1x on
  `inference_geo` requests).

Gateway entries (`gw-*`) copy the price and window of the native entry with the
same `Arg`, because the gateway route carries our prices and the same model
carries the same list price. These are assumptions, not checked against the
gateway's own tariff. The entries for GLM 4.7, Qwen3 Coder and DeepSeek V3 have
no native twin in this catalog, so their prices and window stay 0, which means
unknown.

Retired or superseded models are `Hidden`. A model still listed as current is
visible, including the `[1m]` variant of Opus 5.5, which has the same window as
its base entry.

The host filters the catalog by route policy and builds its selector from it, so
the core stays model-agnostic.

## Compaction

The stream backend implements `contracts.Compactor`, and the manifest declares
`Capabilities.Compactor`. `Compact` sends `/compact`, or `/compact <instruction>`
when the operator gave one, as an ordinary turn on the persistent session. It
reports a note and no context size: the usage of the compaction turn most likely
describes the context before compaction, so the size after it is measured on the
next turn.

`oneshot` has no session to compact and does not implement the port. The
manifest flag covers the whole plugin, so for a `oneshot` instance the instance
does not implement `Compactor`. The contract asks the host to refuse a compaction
of a backend that lacks the port.

## Build and test

```bash
go build ./...
go test ./...
```

Go 1.25, depending only on `herrscher-contracts`. `stream_live_test.go` is gated
behind `CLAUDE_BACKEND_LIVE=1` and shells out to a real `claude`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs), page
  `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts), for the port
  signatures
