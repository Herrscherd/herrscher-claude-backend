package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const transcriptSessionID = "4f1c2b3a-0000-4000-8000-000000000001"

func longInput() string { return strings.Repeat("<x>", 100) }

func longResult() string {
	return "package parser, with a body that is long" + strings.Repeat(" and longer", 10)
}

func transcriptFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"before the boundary"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"tool_use","id":"old","name":"Read","input":{"file_path":"a"}}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"old result"}]},"uuid":"u1","parentUuid":"a0"}`,
		`{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","uuid":"b0","parentUuid":null}`,
		`{"type":"user","message":{"role":"user","content":"fix the parser"},"uuid":"u2","parentUuid":"b0","isCompactSummary":false}`,
		`{"type":"attachment","attachment":{"type":"environment"},"uuid":"x0","parentUuid":"u2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},"uuid":"a1","parentUuid":"x0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reading the file"}]},"uuid":"a2","parentUuid":"a1"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"parser.go"}}]},"uuid":"a3","parentUuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"out.go","content":"` + longInput() + `"}}]},"uuid":"a4","parentUuid":"a3"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + longResult() + `"}]},"uuid":"u3","parentUuid":"a3","toolUseResult":{"kept":"as is"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"written"}],"is_error":true}]},"uuid":"u4","parentUuid":"a4"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a5","parentUuid":"u4"}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"side","name":"Grep","input":{}}]},"uuid":"s0","parentUuid":"a5"}`,
	}, "\n") + "\n"
}

type transcriptCase struct {
	r    *streamResponder
	path string
}

func newTranscriptCase(t *testing.T) transcriptCase {
	t.Helper()
	return newTranscriptCaseWith(t, transcriptFixture())
}

func newTranscriptCaseWith(t *testing.T, transcript string) transcriptCase {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "-work-repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptCase{
		r:    newStreamResponder(t.Context(), nil, "", transcriptSessionID, "", map[string]string{configDirVar: root}),
		path: path,
	}
}

func containsLine(content []byte, line string) bool {
	for _, candidate := range strings.Split(string(content), "\n") {
		if candidate == line {
			return true
		}
	}
	return false
}

func resultOfCall(t *testing.T, r *streamResponder, callID string) string {
	t.Helper()
	history, err := r.ReadHistory(t.Context())
	if err != nil {
		t.Fatalf("ReadHistory after prune: %v", err)
	}
	for _, call := range history.Calls() {
		if call.ID == callID {
			return call.Result
		}
	}
	t.Fatalf("history holds no call %s", callID)
	return ""
}

const (
	imageBlock     = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	referenceBlock = `{"type":"tool_reference","tool_name":"Grep"}`
)

func secondPart() string { return "second part" + strings.Repeat(" again", 15) }

func mixedResultFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"look at the screen"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"tool_use","id":"shot","name":"Screenshot","input":{}}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"shot","content":[{"type":"text","text":"first part"},` + imageBlock + `,` + referenceBlock + `,{"type":"text","text":"` + secondPart() + `"}]}]},"uuid":"u1","parentUuid":"a0"}`,
	}, "\n") + "\n"
}

func prunedResultBlocks(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(transcript)), "\n")
	var entry struct {
		Message struct {
			Content []struct {
				Content []json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err = json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil || len(entry.Message.Content) != 1 {
		t.Fatalf("pruned result line does not decode: %v", err)
	}
	return entry.Message.Content[0].Content
}

func assertPrunedBlocks(t *testing.T, blocks []json.RawMessage, wantText string) {
	t.Helper()
	if len(blocks) != 3 || string(blocks[1]) != imageBlock || string(blocks[2]) != referenceBlock {
		t.Fatalf("pruned blocks = %s, want one text block then the image and the reference untouched", blocks)
	}
	if text, isText := blockOfText(blocks[0]); !isText || text != wantText {
		t.Fatalf("pruned text block = %s, want %q", blocks[0], wantText)
	}
}

func keptOutput() string { return "kept output" + strings.Repeat(" line", 20) }

func rewoundFixture() string {
	return strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"start"},"uuid":"u0","parentUuid":null}`,
		`{"type":"assistant","message":{"id":"m0","role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a0","parentUuid":"u0"}`,
		`{"type":"user","message":{"role":"user","content":"do X"},"uuid":"u1","parentUuid":"a0"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"y1","name":"Bash","input":{"command":"` + longInput() + `"}}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"y1","content":"abandoned output"}]},"uuid":"u2","parentUuid":"a1"}`,
		`{"type":"user","message":{"role":"user","content":"do Y instead"},"uuid":"u3","parentUuid":"a0"}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"y1","name":"Bash","input":{"command":"` + longInput() + `"}}]},"uuid":"a2","parentUuid":"u3"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"y1","content":"` + keptOutput() + `"}]},"uuid":"u4","parentUuid":"a2"}`,
		`{"type":"assistant","message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a3","parentUuid":"u4"}`,
	}, "\n") + "\n"
}
