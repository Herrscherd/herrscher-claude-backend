# Remove DCTL Coupling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans (recommended) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove DCTL-specific behavior from both Claude backend strategies while preserving the public backend API and process invocation semantics.

**Architecture:** `stream` and `oneshot` remain Claude-only implementations behind `contracts.Backend`. `oneshot` continues to pass the prompt through stdin and as the final command argument, but it no longer writes DCTL-specific environment variables; any DCTL adaptation belongs outside this module.

**Tech Stack:** Go, standard library, Go testing, Markdown documentation.

## Global Constraints

- No `DCTL` references remain in Go source or tests.
- `Config`, `NewBackend`, `stream`, and `oneshot` public behavior remains available.
- The test-only live opt-in is `CLAUDE_BACKEND_LIVE=1`.
- The existing command argument and stdin behavior of `oneshot` remains unchanged.

---

### Task 1: Prove oneshot preserves the child environment

**Files:**
- Modify: `backend_test.go` (create if absent; otherwise use the existing backend test file)

- [ ] **Step 1: Write the failing test**

Add a test that sets a sentinel environment variable, invokes `runCmd` with a shell command that prints that variable, and asserts the child sees `preserved` rather than the prompt content.

```go
func TestRunCmdPreservesChildEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_BACKEND_SENTINEL", "preserved")
	got, err := runCmd(context.Background(), "sh -c", contracts.Prompt{Content: `printf '%s' "$CLAUDE_BACKEND_SENTINEL"`})
	if err != nil {
		t.Fatal(err)
	}
	if got != "preserved" {
		t.Fatalf("CLAUDE_BACKEND_SENTINEL = %q, want preserved", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestRunCmdPreservesChildEnvironment -count=1`

Expected: FAIL because `runCmd` currently appends backend-specific environment variables and the shell receives the prompt as its script.

- [ ] **Step 3: Commit the failing test**

```bash
git add backend_test.go
git commit -m "test: prove backend does not inject DCTL environment"
```

### Task 2: Remove DCTL behavior from both backend paths

**Files:**
- Modify: `backend.go`
- Modify: `stream_live_test.go`
- Modify: `backend_test.go`

- [ ] **Step 1: Remove DCTL environment construction**

In `runCmd`, keep `os.Environ()` as the child environment and remove the `DCTL_*` entries. Update the function comment to describe only stdin, final argument, and inherited environment behavior.

- [ ] **Step 2: Rename the live test switch**

Change `DCTL_LIVE` to `CLAUDE_BACKEND_LIVE` in `stream_live_test.go`, including the skip message.

- [ ] **Step 3: Run the focused tests**

Run: `go test ./... -run 'TestRunCmdPreservesChildEnvironment|TestNewBackendSelection|TestNewBackendOneshotRequiresCmd' -count=1`

Expected: PASS.

- [ ] **Step 4: Commit the implementation**

```bash
git add backend.go backend_test.go stream_live_test.go
git commit -m "refactor: decouple Claude backend from DCTL"
```

### Task 3: Remove DCTL references from documentation and verify the repository

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update README**

Remove the DCTL environment-variable block. Describe `oneshot` as invoking a command with the prompt on stdin and as its final argument. Replace attachment examples containing `dctl-attachments` with a neutral temporary attachment path.

- [ ] **Step 2: Run formatting and verification**

Run:

```bash
gofmt -w backend.go backend_test.go stream_live_test.go
go test ./...
go vet ./...
if rg -n "DCTL" --glob '*.go' .; then exit 1; fi
```

Expected: all tests and vet pass, and the final `rg` check produces no matches.

- [ ] **Step 3: Commit documentation and verification changes**

```bash
git add README.md backend.go backend_test.go stream_live_test.go
git commit -m "docs: describe standalone Claude backend"
```
