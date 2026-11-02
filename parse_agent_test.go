package claude

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestParseAgentReadsDescriptionAndType(t *testing.T) {
	t.Parallel()
	got := parseAgent("a1", []byte(`{"description":"reviewer","subagent_type":"general"}`))
	want := contracts.Subagent{ID: "a1", Name: "reviewer", Kind: "general", State: "active"}
	if got != want {
		t.Fatalf("parseAgent = %+v, want %+v", got, want)
	}
}

func TestParseAgentMalformedInputKeepsOnlyIdentity(t *testing.T) {
	t.Parallel()
	got := parseAgent("a2", []byte(`{"description":`))
	want := contracts.Subagent{ID: "a2", State: "active"}
	if got != want {
		t.Fatalf("parseAgent = %+v, want %+v", got, want)
	}
}
