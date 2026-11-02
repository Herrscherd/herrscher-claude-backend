package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestConfigFieldsReachTheStreamChild(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "echo-child.sh")
	child := "#!/bin/sh\nIFS= read -r _\n" +
		`printf '{"type":"result","is_error":false,"result":"%s|%s|%s","session_id":"s"}\n' "$*" "$(pwd -P)" "$HERRSCHER_PROBE"` + "\n"
	if err = os.WriteFile(script, []byte(child), 0o755); err != nil {
		t.Fatal(err)
	}

	backend, err := NewBackend(context.Background(), Config{
		Kind:     "stream",
		Cmd:      script,
		Model:    "claude-x",
		Dir:      dir,
		ResumeID: "conv-9",
		Env:      map[string]string{"HERRSCHER_PROBE": "injected"},
	})
	if err != nil {
		t.Fatal(err)
	}
	responder, ok := backend.(*streamResponder)
	if !ok {
		t.Fatalf("want *streamResponder, got %T", backend)
	}
	t.Cleanup(func() { mustClose(t, responder) })

	got, err := responder.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	want := "-p --input-format stream-json --output-format stream-json --verbose --model claude-x --resume conv-9|" + resolvedDir + "|injected"
	if got != want {
		t.Fatalf("child saw %q, want %q", got, want)
	}
}

func TestStreamFlagSelectsTheStrategyWhenKindIsEmpty(t *testing.T) {
	if _, ok := mustBackend(t, Config{Stream: true, Cmd: "claude"}).(*streamResponder); !ok {
		t.Fatal("Stream: true with no kind should yield the stream strategy")
	}
	if _, ok := mustBackend(t, Config{Cmd: "claude"}).(*oneShotResponder); !ok {
		t.Fatal("Stream: false with no kind should yield the oneshot strategy")
	}
}
