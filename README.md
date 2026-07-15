# herrscher-claude-backend

**The model edge.** This is the only module in the Herrscher platform that knows how
to talk to Claude. It implements the
[`contracts.Backend`](https://github.com/Herrscherd/herrscher-contracts) port: given
one neutral `Prompt`, it returns a reply and streams intermediate progress events
along the way. The core that drives it has no idea Claude exists.

> Part of the Herrscher family: **claude-backend** ·
> [contracts](https://github.com/Herrscherd/herrscher-contracts) ·
> [discord-gateway](https://github.com/Herrscherd/herrscher-discord-gateway) ·
> [obsidian-memory](https://github.com/Herrscherd/herrscher-obsidian-memory) ·
> [orchestrator](https://github.com/Herrscherd/herrscher-orchestrator) ·
> [herrscher](https://github.com/Herrscherd/herrscher) (the umbrella binary that
> imports them all).

```
require github.com/Herrscherd/herrscher-contracts
// the ONLY dependency — no core, no gateway, no host
```

---

## The one entry point

```go
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error)
```

`Config` selects and configures the response strategy:

```go
type Config struct {
    Kind    string // "stream" | "oneshot"; "" resolves from Stream
    Stream  bool   // legacy toggle, consulted only when Kind == ""
    Cmd     string // base command (split on whitespace)
    Model   string // --model value (stream mode)
    Dir     string // working directory ("" = cwd)
    Verbose bool   // backend diagnostics on stderr
}
```

`resolveBackend(kind, stream)` picks the strategy: an explicit `Kind` always wins;
otherwise `Stream == true` ⇒ `"stream"` (the default), `false` ⇒ `"oneshot"`.

---

## Two strategies

### `stream` — one persistent Claude process (default)

`streamResponder` keeps a single `claude` process alive across every message in a
session, speaking Claude Code's **stream-json** protocol over stdin/stdout. This is
what makes a session feel continuous: context, tools and cost accumulate in one
long-lived process rather than starting cold each turn.

The argv it builds (`streamArgv`):

```
claude [your extra args] -p \
  --input-format stream-json --output-format stream-json --verbose \
  [--model <model>] [--resume <session-id>]
```

- **`streamBase`** strips legacy `-p`/`--print`/`--continue` flags from a base
  command so they don't collide with the stream flags.
- **`userLine`** marshals each message into a stream-json user event.
- **`readTurn`** consumes events until the terminal `result`, emitting a
  `contracts.BackendEvent` per intermediate block. It reads with
  `bufio.Reader.ReadBytes` (not `Scanner`) because the init event can exceed 64 KB.
- **Crash recovery:** if the process dies mid-turn, the responder emits a
  `{Kind:"reset"}` event (so the consumer discards partial progress), restarts with
  `--resume <last session id>`, and retries the turn once.

The events it emits map cleanly onto the progress view in the core:

| stream-json | `BackendEvent` |
|-------------|----------------|
| assistant `text` block | `{Kind:"text", Detail:<text>}` |
| assistant `tool_use` block | `{Kind:"tool", Tool:<name>, Detail:<salient input>}` |
| terminal `result` | `{Kind:"result", Cost:<usd>, IsError:<bool>}` |
| process crash | `{Kind:"reset"}` |

`toolDetail` extracts the single most informative input field per tool (the
`command` for Bash, `file_path` for Read/Edit, and so on).

### `oneshot` — run a command per message (legacy / generic)

`oneShotResponder` runs `Cmd` fresh for every message. `runCmd` splits `Cmd` on
whitespace, appends the message text as the final argument, and pipes the full
prompt on stdin. The child process inherits the backend environment unchanged.

`oneshot` requires a non-empty `Cmd`; `NewBackend` returns an error otherwise.

---

## Attachments

`withAttachments` appends downloaded image paths to the message body as plain
references the local Claude can open with its Read tool:

```
look at this

[Image jointe : /tmp/claude-backend-attachments/<session>/a.png]
```

The same helper is shared by both strategies.

---

## The model catalog

The host needs to offer `/session create cmd:` suggestions, but the *core* must stay
model-agnostic — so the catalog lives here and is injected upward.

```go
func CommandPresets(bin string) []contracts.AutocompleteChoice
```

It returns the full **model × effort** matrix as `label → command` autocomplete
choices targeting binary `bin` (e.g. `claude --model claude-opus-4-8 --effort low`).
The host passes the result into `serve.Options.CmdPresets`.

| Models | Effort levels |
|--------|---------------|
| Opus 4.8 · 200k (`claude-opus-4-8`) | low |
| Opus 4.8 · 1M (`claude-opus-4-8[1m]`) | medium |
| Sonnet 4.6 (`claude-sonnet-4-6`) | high |
| Haiku 4.5 (`claude-haiku-4-5-20251001`) | xhigh |
| | max |

The `[1m]` suffix selects the 1M-token context window; its absence means the
standard 200k.

---

## Layout

| File | Contents |
|------|----------|
| `backend.go` | `Config`, `NewBackend`, `resolveBackend`, `runCmd`, the model/effort presets, `CommandPresets` |
| `stream.go` | the stream-json protocol: `streamBase`, `streamArgv`, `userLine`, `readTurn`, `toolDetail`, `withAttachments`, `streamSession`, `streamResponder`, `oneShotResponder` |

> There is no tmux backend. The interactive-TUI strategy was removed; the generic
> select-menu / choice machinery it once needed still lives in the core for future
> use, but no backend here emits choices today.

---

## Build & test

```bash
go build ./...
go vet ./...
go test ./...   # 12 tests
```

Go 1.25. Depends only on the published `herrscher-contracts`. It is a library — the
[herrscher](https://github.com/Herrscherd/herrscher) umbrella is the only binary that
imports it.
