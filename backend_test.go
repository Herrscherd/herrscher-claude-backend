package claude

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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

func TestConfigHasNoUnreadFields(t *testing.T) {
	want := map[string]bool{
		"Kind": true, "Stream": true, "Cmd": true,
		"Model": true, "Dir": true, "ResumeID": true,
		"Env": true,
	}
	fields := configFieldNames(t)
	for _, name := range fields {
		if !want[name] {
			t.Fatalf("Config.%s is not read anywhere; remove it or wire it up", name)
		}
	}
	if len(fields) != len(want) {
		t.Fatalf("Config has %d fields, want %d", len(fields), len(want))
	}
}

func TestClaudeBackendsAreSkillNative(*testing.T) {
	var _ contracts.SkillNative = (*streamResponder)(nil)
	var _ contracts.SkillNative = (*oneShotResponder)(nil)
}

func configFieldNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "backend.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing backend.go: %v", err)
	}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Config" {
			return true
		}
		structType, isStruct := spec.Type.(*ast.StructType)
		if !isStruct {
			return false
		}
		for _, field := range structType.Fields.List {
			for _, name := range field.Names {
				names = append(names, name.Name)
			}
		}
		return false
	})
	return names
}
