# Remove DCTL coupling from the Claude backend

## Goal

Keep this module reusable as a standalone Claude backend. The package must not
know about DCTL-specific environment variables or DCTL-specific test switches.

## Design

- Keep both backend strategies, `stream` and `oneshot`.
- Keep the neutral `contracts.Prompt` input and Claude process behavior.
- Remove all `DCTL_*` environment variables from `runCmd`; `oneshot` passes the
  prompt through stdin and the existing command argument only.
- Remove `DCTL_LIVE` from the live test and use `CLAUDE_BACKEND_LIVE` as a
  backend-local opt-in.
- Update comments and README examples so no DCTL-specific protocol is claimed
  by this repository. Attachment references remain backend-neutral.

## Compatibility

This intentionally removes the implicit DCTL environment-variable contract.
The DCTL bridge must perform any DCTL-to-process adaptation outside this module.
The public `Config`, `contracts.Backend`, and both strategy names remain
unchanged.

## Verification

- Add a regression test proving the oneshot child does not receive `DCTL_*`
  variables from the backend.
- Run the package test suite and `go vet ./...`.
- Confirm `rg 'DCTL'` has no matches in the repository.
