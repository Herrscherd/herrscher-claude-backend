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

Gateway entries (`gw-*`) carry no price. The gateway route bills at our price,
not Anthropic's (see the `InputPrice` contract on `ModelSpec`), and that price is
not published in this catalog. Copying the native list price would show a price
that is not ours, so their four price fields stay 0, which means unknown. Their
context window is the window of the native entry with the same `Arg`, assumed to
hold on the gateway as well. GLM 4.7, Qwen3 Coder and DeepSeek V3 have no native
twin in this catalog, so their window stays 0.

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
next turn. Its note is English, as the host reads it today. Its `Notice` is
`compaction.done`, with no argument, and a failure is a `compact.unavailable`
notice whose `Reason` is the first non blank line of the cause, cut to 200 runes
with `…` when longer. `Error()` keeps the full cause for the logs.

`oneshot` has no session to compact and does not implement the port. The
manifest flag covers the whole plugin, so for a `oneshot` instance the instance
does not implement `Compactor`. The contract asks the host to refuse a compaction
of a backend that lacks the port.

## History editing

The stream backend also implements `contracts.HistoryEditor`, declared by
`Capabilities.HistoryEditor`, so a compaction stage such as Jev can prune tool
calls without a summary. The claude CLI keeps each session in
`<config>/projects/<project>/<session>.jsonl`, where `<config>` is
`CLAUDE_CONFIG_DIR` from the injected env, then from the environment, then
`~/.claude`. The session id must look like one the CLI writes, so it never
widens the lookup.

`ReadHistory` reads the file as a stream, up to the size it had when it was
opened, and keeps the conversation after the last `compact_boundary`. It walks
`parentUuid` back from the last entry that is not a sidechain, so a branch left
behind by a rewind stays out; the result entries of parallel calls, which hang
off their own `tool_use` entry rather than off the chain, are taken in with it.
The per block assistant entries of one API message are joined back into one
message with its calls and their results, even when the CLI writes a result
between two of them; a user entry with real text ends the message.

`PruneHistory` checks every edit first, against the contract and against the
calls of that history, and changes nothing when one fails. It edits only
`message.content`, and only on the lines of that chain: `prune_result` keeps the
first `KeepChars` runes of a result and a note of what was pruned,
`prune_call` replaces the result by a note and cuts the long strings of the call
input to 200 runes. A result made of blocks keeps its images, tool references
and every other non text block in place and in order: only the text blocks are
rewritten, the rune budget runs across them in order, and the note closes the
last text block kept. No entry is added or removed and no `uuid` or
`parentUuid` moves, so the chain stays intact. The new `message.content` is
spliced into the raw line, so the other fields keep their bytes and their key
order; only a shortened call input is encoded again, with its keys sorted. Every
other line is copied byte for byte. A cancelled context stops the read or the
rewrite between two lines and leaves the transcript as it was.

A prune never grows the history and running it twice changes nothing. A result
that already ends with a herrscher note, after no more than the runes the edit
keeps, is left alone by `prune_result`, and a result or an input is rewritten
only when the new one is shorter, so a short result keeps its text rather than
gain a longer note. Every call counted in `Edited` is shorter after the prune;
when no call changes, the transcript is not written and the live claude keeps
running.

When something changes, the live claude process is closed first and the
transcript is read again, so nothing claude writes while it exits is lost. The
new file is staged next to the old one and synced, the old one is kept as a hard
link that costs no copy, the new one is renamed over it and the directory is
synced. Two backups at most stay next to a transcript:
`<session>.jsonl.herrscher-bak` holds the transcript before the first prune and
is never overwritten, and `<session>.jsonl.herrscher-bak.prev` holds it before
the latest prune, replaced by a rename at each later one. The size they keep is
logged at the info level. Removing both backups is safe once the pruned
conversation is trusted.

Backups are hard links, so the transcript must live on a file system that has
them: on exFAT, vfat or some FUSE and SMB mounts the prune stops with
`ErrTranscriptBackup` and leaves the transcript as it was. Staged files carry
their creation time and a random suffix, so two prunes of one session never
write the same file; one left behind by a crash is removed by the first prune
that finds it more than an hour old. A failed sync of the directory after the
rename is logged as a warning: the prune is done, only a crash right after it
could bring the old transcript back.

The next turn respawns claude with `--resume`, which reads the pruned history;
a prune that fails after closing claude keeps the session id, so that turn
still resumes the same session.

Another writer may still hold the session, such as a `claude --resume` run in a
terminal, which appends to the same file. The size, modification time and inode
read are compared again when the prune starts and right before the rename; on
any change the prune stops with `ErrTranscriptChanged`, removes its staged file
and leaves the transcript alone. A write landing between that last check and
the rename is the one case it cannot see: it lands in the backup, not in the
live file.

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
