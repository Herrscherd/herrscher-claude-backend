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
vendor login, `gateway` uses the product's account.

The host filters the catalog by route policy and builds its selector from it, so
the core stays model-agnostic.

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
