# herrscher-claude-backend

**The model edge for Claude.** Turns one neutral `contracts.Prompt` into a reply,
streaming intermediate progress events (text, thinking, tool calls, token usage,
cost) as they arrive. It is a library, not a binary, and it is the only module in
the platform that knows Claude exists.

## Role · Category · Ports · Config · Status · Repo

| Aspect | Value |
|--------|-------|
| **Role** | Drives the local `claude` CLI and maps its stream-json output onto backend events |
| **Category** | Backend (model edge) |
| **Ports implemented** | `Backend`, `ResumeAware`, `SkillNative` |
| **Config & env** | `CLAUDE_CMD` (default: `claude`), `CLAUDE_MODEL`, `CLAUDE_STREAM` (default: `true`; `false` selects oneshot), `CLAUDE_DIR`, `CLAUDE_KIND` (`stream`\|`oneshot`); plus `env` — declared with no env binding and injected by the host per session (`K=V` per line, merged onto every spawned child), never read from the daemon's environment |
| **Status** | live |
| **Repo** | [herrscher-claude-backend](https://github.com/Herrscherd/herrscher-claude-backend) |

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-claude-backend
```

## Two strategies

`stream` (default) keeps one persistent `claude` process alive per session and
speaks stream-json over stdin/stdout, so context, tools and cost accumulate
across turns. If the process dies mid-turn it emits `{Kind:"reset"}`, restarts
with `--resume <session id>`, and retries once.

`oneshot` runs `CLAUDE_CMD` fresh for every message, with the content appended as
the final argument and piped on stdin. It requires a non-empty command.

## Resume and memory

The current claude session id is exposed through `ResumeToken()`. The host
persists it and feeds it back at construction as the `resume` setting — that key
is read from `PluginConfig` but is deliberately not a user-facing manifest
setting or env var.

`Prompt.Context` (memory recall) is prepended inside a `<memory data-only="true">`
fence; any `<memory>` tag the recalled text carries is neutralized first so it
cannot forge or close the fence.

## Model catalog

`Models` is the declared catalog, published through `Manifest.Models`: each
entry carries its id, label, efforts and its `Route` (`native` — the machine's
own vendor login — or `gateway` — the product's account). The host filters it
by route policy and builds its selector from it, so the core stays
model-agnostic. There is no `CommandPresets` helper here; it was removed in
`ac57041` when the catalog took over.

## Build & test

```bash
go build ./...
go test ./...
```

Go 1.25, depending only on `herrscher-contracts`. `stream_live_test.go` is gated
behind `CLAUDE_BACKEND_LIVE=1` and shells out to a real `claude`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs) — `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts) — port signatures
