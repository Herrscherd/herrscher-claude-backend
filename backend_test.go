package claude

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestRunCmdPreservesChildEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_BACKEND_SENTINEL", "preserved")
	got, err := runCmd(context.Background(), "sh -c", nil, contracts.Prompt{Content: `printf '%s' "$CLAUDE_BACKEND_SENTINEL"`})
	if err != nil {
		t.Fatal(err)
	}
	if got != "preserved" {
		t.Fatalf("CLAUDE_BACKEND_SENTINEL = %q, want preserved", got)
	}
}

func TestRunCmdIncludesAttachmentsInPrompt(t *testing.T) {
	got, err := runCmd(context.Background(), "printf %s", nil, contracts.Prompt{
		Content:     "look",
		Attachments: []string{"/tmp/a.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "look\n\n[Image jointe : /tmp/a.png]"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestClaudeBackendsAreSkillNative(t *testing.T) {
	for _, kind := range []string{"stream", "oneshot"} {
		backend, err := NewBackend(context.Background(), Config{Kind: kind, Cmd: "claude"})
		if err != nil {
			t.Fatalf("NewBackend(%s): %v", kind, err)
		}
		skilled, ok := backend.(contracts.SkillNative)
		if !ok || !skilled.NativeSkills() {
			t.Fatalf("%s backend does not report native skills", kind)
		}
	}
}
