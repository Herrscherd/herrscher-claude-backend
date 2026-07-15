# Claude backend review — 2026-07-15

Scope: the two commits ahead of `origin/master`, plus the current backend
module. Findings below are limited to behavior verified in the repository.

## Verification

- `go test ./...` — passed
- `go test -race ./...` — passed
- `go vet ./...` — passed
- `git diff --check` — passed
- No `DCTL` references remain in Go source or tests
- Coverage: 54.8% before the added attachment test; no coverage gate is present

## Confirmed finding fixed

### Medium — oneshot dropped attachments after DCTL removal

`runCmd` previously relied on the DCTL attachment environment variable. After
removing that coupling, the oneshot path passed only `Context` and `Content`,
while the stream path also called `withAttachments`. This silently made
attachments unavailable to oneshot consumers.

Fix: `runCmd` now builds its content with `withAttachments` before applying
context, matching the stream path. `TestRunCmdIncludesAttachmentsInPrompt`
covers the behavior.

## Review by axis

### CI compliance

No `.github/workflows`, Makefile, Taskfile, or linter configuration exists in
this repository. The Go checks above pass, but there is no repository-local CI
definition to validate or enforce them. Adding a minimal Go CI workflow is a
proposal, not a reported failing check.

### Architecture

The DCTL coupling is removed from both runtime paths. The DCTL-to-process
adaptation can now live in the bridge layer.

One pre-existing contract ambiguity remains: the README and comments describe
stream as the default, but `NewBackend(Config{})` resolves the zero-value
`Stream == false` to oneshot and then errors because `Cmd` is empty. This needs
an explicit product decision about zero-value semantics; it is not changed here
because changing it could break legacy callers.

### Performance

No confirmed regression. The environment is still copied once per oneshot
process, and stream sessions remain serialized as required by one persistent
process. No speculative optimization is recommended.

### Code quality

No confirmed correctness issue. The added regression tests are focused and use
real child processes. Stale comments about the bridge and generic oneshot
behavior were removed or made accurate.

### Security

No confirmed issue in the reviewed change. `exec.Command` remains used without
an implicit shell, and the child environment is inherited unchanged as
documented. The configurable command is intentionally executable by the host;
it should not be treated as untrusted user input.

### Bug review

The attachment regression was the only confirmed bug found in the reviewed
change. Tests pass under the race detector and no additional reproducible bug
was identified.

### Documentation drift

README now reflects the current `contracts.Choice` return type, all model ×
effort combinations, the current 17-test suite, neutral attachment paths, and
the DCTL-free oneshot behavior.

## Proposals

1. Add repository CI for `go test ./...`, `go test -race ./...`, and
   `go vet ./...`.
2. Decide and document whether `Config{}` must select stream or whether the
   legacy zero-value behavior is intentional; add a test for that decision.
