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
| `CLAUDE_KIND` | | `stream` or `oneshot`, explicit form of `CLAUDE_STREAM` |

There is one more setting, `env`, and it has no environment binding on purpose.
The host injects it per session, `K=V` per line, merged onto every spawned
child. It is never read from the daemon's own environment.

## Two strategies

`stream`, the default, keeps one persistent `claude` process alive per session
and speaks stream-json over stdin and stdout, so context, tools and cost
accumulate across turns. A turn counts as taken by the CLI once the stream has
carried an `assistant` or `user` line whose header decodes, whatever its
content, or a `system` line of subtype `init` or `api_retry`. That list is
closed: every other `system` line, such as the `hook_started` lines of hooks,
and every line of an unknown type or that does not decode, leaves the turn not
taken. Measured on the CLI 2.1.322: a fresh start, with stdin open and no prompt
written, was killed after 10 seconds, and it wrote its SessionStart hook lines
at startup and no `init` line, so the hook lines do not count. A fresh start of
the same CLI that was given a prompt wrote its `init` line 67 ms after the
prompt was written to stdin, so a line of that subtype takes the turn on a fresh
start; that it is never written before the prompt is read is deduced. The
`api_retry` subtype is deduced, not measured: it follows an API request, and
that request carries the prompt. If the process dies before the turn is taken,
the responder relaunches it, and the deaths are counted per death, not per turn.
A silent death of a resumed session whose id this process has seen accepted
emits `{Kind:"reset"}` and keeps the id, relaunching with
`--resume <session id>`. A second silent death on that same id forgets the id
and emits `{Kind:"notice"}` carrying `CodeResumeLost`, then `{Kind:"reset"}`:
the sequence is reset, notice, reset, and a fresh session answers the same turn.
A resumed id that this process has never seen accepted is forgotten at its first
silent death, with the same notice, `reset` and fresh fallback. A fresh session
that dies before any line is relaunched once, without `--resume`, with the
`reset` event and no notice, since no resume was lost. A `result` error, while
the turn is not yet taken, on the first turn that a process started with
`--resume` answers, is a refusal: the id is forgotten, and a fresh session
answers the same turn with the notice and the `reset` event. A refusal gives
that same notice and `reset`. When the refused turn had already relaunched after
a silent death, that death's `reset` comes first, so the sequence is reset,
notice, reset. An accepted resume that dies once is relaunched with a reset
event, no notice. A turn that ends in `result` error ends its session: once the
turn is over, the responder stops the process, even one that is still alive,
and keeps the session id that was published. The next turn relaunches with
`--resume <session id>` when an id was published, and as a fresh launch when
none was. That relaunch emits no `reset` event and counts no death. The cost is
assumed: after each application error, even when the CLI was healthy, the next
turn pays one extra launch. A turn makes at most three launches, so the prompt
is written at most three times. If the last launch also ends the stream before
the turn is taken,
the turn ends with an error that wraps `io.EOF` and whose text, in the prompt's
language, says that the stream ended before the turn finished, and the text
already rendered stays rendered. The next turn restarts from the last session id
that was published, with `--resume <session id>`. An id is published when a line
that takes the turn carries it, when a turn succeeds, when a turn ends in error
(the session is then dropped, and its id is published with it), when a session
is dropped after a counted silent death, or when a history prune drops the
session. The id of a lost resume is never published: the responder forgets it.
The id of any other line is not recorded, a hook line included: the CLI 2.1.322
writes a `session_id` on its `hook_started` lines, measured by a run killed
before any prompt was written, and whether that session is already persisted
when the hook runs is deduced, not measured, so resuming it could fail. The id
of an `init` line is recorded, since that line is written once the prompt has
been given (deduced, not measured); that the session is already persisted when
it is written is deduced, not measured, the same as for a hook line. The
difference is that `init` is written once the CLI has the prompt (deduced, not
measured), while a hook line is written before. How the CLI answers a `--resume`
on an id it has not persisted is not measured. A session that has recorded none
restarts with the id it was started with, if any. If the relaunches also end the
stream before the turn is taken, the error is the same. Two windows remain in
which a relaunch with `--resume` can put the prompt twice in the resumed
history: a death after the CLI has read the prompt but before any line of the
list above, and a death after a line outside that list that the CLI wrote once
it had read the prompt. No tool can run in these two windows, since a tool use
reaches the stream in an assistant line first and that line is taken; this is
deduced, not measured.

A third window would lose the prompt instead of doubling it. The order of the
lines on `--resume` is not measured: if the CLI wrote `init` before reading the
prompt, a death between the write of `init` and the read of the prompt would
count as a taken turn and drop the relaunch.

`Close` interrupts the turn in flight: it kills the `claude` process and, on
Unix, every process still in its process group, without waiting for the turn to
end. The `Respond` or `Compact` call then returns `ErrResponderClosed` with no
retry. A turn whose `result` line the reader has already received is rendered
whole, with no error, even when `Close` arrives first: nothing is lost and
nothing is replayed. Once the responder is closed, every `Respond` and `Compact`
returns `ErrResponderClosed` without starting a process. The text of that error,
like the stream-ended text, is in the prompt's language, English when it is
unknown. Every `Close` returns only once the process it stopped is killed and
reaped, unless it gives up on a spawn as described next, so a second concurrent
`Close` waits for the same teardown too. A spawn in flight when `Close` runs is
reaped as soon as it returns; if it has not returned after 5 seconds, `Close`
gives up and returns a teardown timeout error, and the spawn is still reaped
when it does return. That error is meant for logs only: the herrscher host never
shows it to a user. It is logged at WARN for a session backend, at ERROR for a
plugin-hosted backend, and at DEBUG for an approval check, which the default
Info level does not write. A session that `install` replaces is killed, never
left running. A stream line over 16 MiB (`ErrStreamLineTooLong`) or an event
that no longer decodes (`ErrStreamDrift`) ends the turn with an error and is
never retried: the CLI may already have acted on the turn, and resending the
prompt would duplicate it. Once a line is complete, the reader holds at most 16
MiB of it; while the next line is being read, it grows up to 16 MiB too, and the
4 KiB read buffer is held in addition. An over-bound line is reported at most 4
KiB past the bound: at the first line end after the bound, when the 4 KiB read
buffer fills with no line end, or at the end of the stream. If the context of
the turn is cancelled before that, the turn ends with the context error and no
over-bound finding is made. The `claude` process is always reaped once its
session or turn ends. On Unix it is started as the leader of its own process
group, and that group is killed on `Close`, when the context of a session or of
a oneshot turn is cancelled, and after a oneshot `claude` process exits. A tool,
MCP server or helper it spawned that is still in the group does not outlive the
session. A descendant that calls `setsid` leaves the group, is not reached, and
can outlive the session. On Windows only the `claude` process itself is killed,
and its descendants can outlive it.

The group is signalled by its id in two ways. `Close`, `killAndReap` and the
cancellation of the context (`cmd.Cancel`) signal it before the leader is
reaped: the `claude` process is then still a zombie, which keeps its pid, and so
the group id, from being reused. Only `runGroup` (the oneshot turn and the
health probe) signals it after `Wait` has reaped the leader. From that point
Linux does not hand out an id while a live process of the group still uses it,
so the group is protected as long as one of its members lives. The one
remaining risk is that the group has already emptied when the signal is sent
and another process group takes the same id before it: that group would then
be signalled.

Output pipes are read until they close, and the wait for them is bounded by
`groupWaitDelay`, one second, after the `claude` process exits or is cancelled.
A oneshot turn whose process exits successfully while a descendant still holds
its stdout open keeps the output it has read and returns no error. Such a turn
returns about one second after the exit, then the group is killed. The health
probe has the same one-second bound. The worst case is 40 MiB of line buffers
per session: while a line grows to its bound, the reader keeps its previous 8
MiB buffer alive beside the new 16 MiB one, on top of the consumer's full line.

`oneshot` runs `CLAUDE_CMD` fresh for every message, with the content appended
as the final argument and piped on stdin. It requires a non-empty command.

## Text events

A `text` event that follows another `text` event in the same attempt starts with
`"\n\n"`, and the first text of an attempt has no prefix. Consumers concatenate
the details as they arrive and add nothing between them. A `reset` is emitted
only before any text of the turn, so no text precedes it: without earlier text,
there is nothing to throw away. The text that follows a `reset`, from the
retried attempt, is the first text of that attempt and has no prefix. After an
over-bound line, the text already emitted stays with the consumer, with no
`reset`, and the error follows it.

## Resume and memory

The current claude session id is exposed through `ResumeToken()`. The host
persists it and feeds it back at construction as the `resume` setting. That key
is read from `PluginConfig` but is deliberately not a user-facing manifest
setting, and not an environment variable.

`Prompt.Context`, which is the memory recall, is prepended inside a
`<memory data-only="true">` fence. Any `<memory>` tag the recalled text carries
is neutralized first, so it cannot forge or close the fence.

## Tool approvals

When a turn carries an approval asker in its context, the backend opens a
private Unix socket and exports its path as `HERRSCHER_APPROVAL_SOCKET` to the
claude process. A tool hook can then ask the host for a decision by sending the
request to that socket, and the answer comes from the asker of the running turn.

The socket lives in a directory created with mode 0700 and is served by
`approvalsock`, which accepts only the owner's connections. The socket is bound
once per session and reused across the turns and relaunches of that session.

A request is answered only while a turn that carries an asker is running. Outside
such a turn, or when the request does not carry the ID of the question being
asked, the socket answers with a denial that gives the reason. This path does
not send the session token.

A session launched by a turn without an asker has no approval socket. Its
environment does not carry `HERRSCHER_APPROVAL_SOCKET`, and a later turn with an
asker on that session is refused with `ErrApprovalUnreachable`, so the tool hook
never runs without a channel to the daemon. Start a new session to get one.

The one-shot backend follows the same rules for each run: the socket exists only
for a run that carries an asker, and it is removed when the run ends.

## Model catalog

`Models` is the declared catalog, published through `Manifest.Models`. Each
entry carries its id, label, efforts and its `Route`: `native` uses the
machine's own vendor login, `gateway` uses the product's account. `Arg` is what
goes on the CLI's `--model`, and `ID` is what the host persists.

Each native entry carries its price in USD per million tokens (input, output,
cache read, and the 5-minute cache write) and its context window in tokens. The
values are the base tier of the Anthropic pricing page
(https://platform.claude.com/docs/en/about-claude/pricing) and the model pages
of the Anthropic docs. Haiku 4.5 is 200K tokens, every other model listed is 1M.

What the catalog does not model:

- The 1-hour cache write. Only the 5-minute write goes in `CacheWritePrice`.
- The Claude Haiku 5.5 long-prompt tier. A prompt over 100,000 tokens costs
  $0.50 input, $0.625 5-minute cache write, $1 1-hour cache write, $0.05 cache
  read and $2.50 output. The contract has no tier field, so the entry carries
  the base tier only, and a request over 100,000 tokens is under-priced by a
  factor of five.
- The batch discount (50%) and the US-only inference multiplier (1.1x on
  `inference_geo` requests).

Gateway entries (`gw-*`) carry no price. The gateway route bills at our price,
not Anthropic's (see the `InputPrice` contract on `ModelSpec`), and that price
is not published in this catalog. Copying the native list price would show a
price that is not ours, so their four price fields stay 0, which means unknown.
Their context window is the window of the native entry with the same `Arg`,
assumed to hold on the gateway as well. GLM 4.7, Qwen3 Coder and DeepSeek V3
have no native twin in this catalog, so their window stays 0.

Retired or superseded models are `Hidden`. A model still listed as current is
visible, including the `[1m]` variant of Opus 5.5, which has the same window as
its base entry.

The host filters the catalog by route policy and builds its selector from it, so
the core stays model-agnostic.

## Compaction

The stream backend implements `contracts.Compactor`, and the manifest declares
`Capabilities.Compactor`. `Compact` sends `/compact`, or `/compact
<instruction>` when the operator gave one, as an ordinary turn on the persistent
session. It reports a note and no context size: the usage of the compaction turn
most likely describes the context before compaction, so the size after it is
measured on the next turn. Its note is English, as the host reads it today. Its
`Notice` is `compaction.done`, with no argument, and a failure is a
`compact.unavailable` notice whose `Reason` is the first non blank line of the
cause, cut to 200 runes with `…` when longer. `Error()` keeps the full cause for
the logs. A `Compact` interrupted by `Close`, or made after it, fails with
`ErrResponderClosed` as its cause. The closed and stream-ended texts of a failed
compaction are in the language of `CompactRequest.Session`, English when that
language is unknown.

`oneshot` has no session to compact and does not implement the port. The
manifest flag covers the whole plugin, so for a `oneshot` instance the instance
does not implement `Compactor`. The contract asks the host to refuse a
compaction of a backend that lacks the port.

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

An assistant entry the CLI flags with `isApiErrorMessage` is left out, as the
stream leaves its text out: a history has no role for an error, and the stream
reports that error as the failure of its turn, not as a reply. The omitted entry
leaves no placeholder, so when a user entry follows it, two user messages can
sit next to each other in the history.

`PruneHistory` checks every edit first, against the contract and against the
calls of that history, and changes nothing when one fails. It edits only
`message.content`, and only on the lines of that chain: `prune_result` keeps the
first `KeepChars` runes of a result, the last `KeepTail` runes, and a note of
how many runes were pruned between them, while `prune_call` replaces the result
by a note, cuts the long strings of the call input to 200 runes, and refuses a
`KeepTail`. A result that the head and the tail cover whole is left as it is.
Runes are counted, never bytes, and a rune is never split. A result made of
blocks keeps its images, tool references and every other non text block in place
and in order: only the text blocks are rewritten, the head budget runs across
the text blocks in order, the tail budget runs back from the last one, the note
sits in the first text block cut, and text blocks wholly inside the pruned span
are dropped. No entry is added or removed and no `uuid` or `parentUuid` moves,
so the chain stays intact. The new `message.content` is spliced into the raw
line, so the other fields keep their bytes and their key order; only a shortened
call input is encoded again, with its keys sorted. Every other line is copied
byte for byte. A cancelled context stops the read or the rewrite between two
lines and leaves the transcript as it was.

A prune never grows the history and running it twice changes nothing. A result
that already holds a herrscher note, with a head and a tail no longer than the
runes the edit keeps, is left alone by `prune_result`, and a result or an input
is rewritten only when the new one is shorter, so a short result keeps its text
rather than gain a longer note. A result that holds a note is pruned again
without any fragment of that note: the head stops before the first note, the
tail starts after the last one, and the count adds the runes cut now to the
counts of the notes it replaces, so it still gives what is missing from the
original result. A head that meets a note early stays shorter than asked. Every
call counted in `Edited` is shorter after the prune; when no call changes, the
transcript is not written and the live claude keeps running.

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

## Health and limits

`Probe` checks one account without a live instance. It runs `claude auth status`
with the account environment, the profile env merged over the host env. Logged
out is `off`, with the remedy `herrscher account add claude <name>` for a
profile or `claude auth login` for the native account. Logged in is `ok` with
the latency of the check. With `Deep` it also runs one turn of one token on the
cheapest native model, with no tool: a rate limit is `degraded`, refused
credentials are `down`, a server error is `degraded`, and a missing CLI is
`down`.

`Limits` reads `GET https://api.anthropic.com/api/oauth/usage` with the OAuth
access token stored under `claudeAiOauth` in `<config>/.credentials.json`, where
`<config>` is the same directory as in the history section. It maps `five_hour`,
`seven_day` and the weekly windows of each model family, whether they come as
`seven_day_<family>` or in the `limits` array, to `contracts.Limit`. The
`seven_day_<family>` keys are kept only for the families of the model catalogue
(`Models`); other keys such as `seven_day_oauth_apps` are ignored. A response
with no known window, an unreadable body, an absent, expired or refused token, a
429 or a 5xx all return an error matching `ErrLimitsUnavailable`. The token is
never logged and never put in an error. The HTTP client has no timeout of its
own: its deadline is the context of the caller.

The endpoint is not a documented API: its shape is the one the claude CLI reads,
and the CLI itself marks it experimental. `Models` on a result event comes from
the `modelUsage` of the result message, one entry per model of the turn.

## Security

Every process this backend starts goes through `newGroupCommand` in
`process_group.go`. Its contract is that it runs only commands the operator
configured: the program name and the configured arguments of `CLAUDE_CMD`, of
the stream command and of the health probe. Turn content is never the program
name. A oneshot turn passes its content as the last argument element and on
stdin, so the contract covers the program name and the configured arguments, not
the values of the arguments that carry the turn.

Because of this contract, gosec rule G204 (command started with variable
arguments) is excluded for `process_group.go` only. Its callers, `runCmd`, the
stream session and the health probe, are no longer checked by G204, so a future
caller that builds the program name from turn content would not be flagged by
the linter. Such a change must be refused in review.

## Build and test

```bash
go build ./...
go test ./...
```

Go 1.27.0, depending only on `herrscher-contracts`. `stream_live_test.go` is
gated behind `CLAUDE_BACKEND_LIVE=1` and shells out to a real `claude`.

## Further reading

- [Herrscher docs](https://github.com/Herrscherd/herrscher-docs), page
  `plugins/backend`
- [contracts](https://github.com/Herrscherd/herrscher-contracts), for the port
  signatures
