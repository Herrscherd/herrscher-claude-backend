package claude

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestStreamArgv(t *testing.T) {
	got := streamArgv([]string{"claude", "--permission-mode", "acceptEdits"}, "claude-haiku", "sess-9")
	want := []string{
		"claude", "--permission-mode", "acceptEdits",
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--model", "claude-haiku",
		"--resume", "sess-9",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv =\n  %v\nwant\n  %v", got, want)
	}

	bare := streamArgv([]string{"claude"}, "", "")
	for _, f := range bare {
		if f == "--model" || f == "--resume" {
			t.Fatalf("did not expect %q in %v", f, bare)
		}
	}
}

func TestStreamSessionSend(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go func() {
		br := bufio.NewReader(stdinR)
		if _, err := br.ReadBytes('\n'); err != nil {
			return
		}
		mustWriteString(t, stdoutW,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`+"\n"+
				`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n"+
				`{"type":"result","subtype":"success","is_error":false,"result":"hello back","total_cost_usd":0.002,"session_id":"abc"}`+"\n")
		mustClose(t, stdoutW)
	}()

	s := newStreamSession(stdinW, stdoutR)
	var got []contracts.BackendEvent
	tr, err := s.Send(context.Background(), "hello", func(e contracts.BackendEvent) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "hello back" {
		t.Fatalf("text = %q, want 'hello back'", tr.Text)
	}
	if s.sessID != "abc" {
		t.Fatalf("session id not recorded: %q", s.sessID)
	}
	if len(got) < 1 || got[0].Kind != "tool" || got[0].Tool != "Bash" {
		t.Fatalf("expected a Bash tool event, got %+v", got)
	}
}
