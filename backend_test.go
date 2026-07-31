package claude

import (
	"context"
	"reflect"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

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

func TestRunCmdIncludesAttachmentsInPrompt(t *testing.T) {
	got, err := runCmd(context.Background(), "printf %s", contracts.Prompt{
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

// TestConfigHasNoUnreadFields pins the Config surface: every field must be read
// somewhere in the package. Verbose used to sit here purely as a "reserved"
// knob that nothing consulted, so callers setting it got silence.
func TestConfigHasNoUnreadFields(t *testing.T) {
	want := map[string]bool{
		"Kind": true, "Stream": true, "Cmd": true,
		"Model": true, "Dir": true, "ResumeID": true,
	}
	ty := reflect.TypeOf(Config{})
	for i := 0; i < ty.NumField(); i++ {
		if name := ty.Field(i).Name; !want[name] {
			t.Fatalf("Config.%s is not read anywhere; remove it or wire it up", name)
		}
	}
	if ty.NumField() != len(want) {
		t.Fatalf("Config has %d fields, want %d", ty.NumField(), len(want))
	}
}

func TestClaudeBackendsAreSkillNative(t *testing.T) {
	var _ contracts.SkillNative = (*streamResponder)(nil)
	var _ contracts.SkillNative = (*oneShotResponder)(nil)
}
