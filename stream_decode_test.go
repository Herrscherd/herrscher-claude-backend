package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestReadTurnCtxCancelAborts(t *testing.T) {
	pr, _ := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := readTurn(ctx, startLineReader(pr, nil), nil, nil)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readTurn did not abort on ctx cancel")
	}
}

func TestReadTurnSuccess(t *testing.T) {
	canned := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-haiku"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]},"session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"PONG"}]},"session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"PONG","total_cost_usd":0.0136,"session_id":"sess-1"}`,
	}, "\n") + "\n"

	tr, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "PONG" {
		t.Fatalf("text = %q, want PONG", tr.Text)
	}
	if tr.CostUSD <= 0 {
		t.Fatalf("cost = %v, want > 0", tr.CostUSD)
	}
	if tr.SessionID != "sess-1" {
		t.Fatalf("session id = %q, want sess-1", tr.SessionID)
	}
	if tr.IsError {
		t.Fatal("did not expect error")
	}
}

func TestReadTurnError(t *testing.T) {
	canned := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","session_id":"s"}` + "\n"
	tr, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.IsError {
		t.Fatal("expected IsError")
	}
	if tr.ErrMsg == "" {
		t.Fatal("expected ErrMsg populated")
	}
}

func TestReadTurnHandlesHugeLine(t *testing.T) {
	huge := strings.Repeat("x", 200_000)
	canned := `{"type":"system","subtype":"init","session_id":"s","blob":"` + huge + `"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	tr, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, nil)
	if err != nil {
		t.Fatalf("huge line should not error: %v", err)
	}
	if tr.Text != "ok" {
		t.Fatalf("text = %q, want ok", tr.Text)
	}
}

func TestReadTurnEmitsEvents(t *testing.T) {
	canned := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"je réfléchis"}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"je regarde"}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"stream.go"}}]},"session_id":"s"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.04,"session_id":"s"}`,
	}, "\n") + "\n"

	var got []contracts.BackendEvent
	tr, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, func(e contracts.BackendEvent) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "done" {
		t.Fatalf("text = %q, want done", tr.Text)
	}
	want := []contracts.BackendEvent{
		{Kind: "thinking", Detail: "je réfléchis"},
		{Kind: "text", Detail: "je regarde"},
		{Kind: "tool", Tool: "Bash", Detail: "git status"},
		{Kind: "tool", Tool: "Read", Detail: "stream.go"},
		{Kind: "result", Cost: 0.04, IsError: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !sameEvent(t, got[i], want[i]) {
			t.Fatalf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadTurnSurfacesCacheTokens(t *testing.T) {
	canned := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":30,"output_tokens":20,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}},"session_id":"s"}
{"type":"result","is_error":false,"result":"hi","total_cost_usd":0.004,"session_id":"s","usage":{"input_tokens":30,"output_tokens":55,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}}
`

	var last contracts.BackendEvent
	res, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, func(e contracts.BackendEvent) {
		if e.Kind == "result" {
			last = e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheRead != 12 || res.CacheCreate != 3 {
		t.Fatalf("turnResult cache lost: %+v", res)
	}
	if last.CacheRead != 12 || last.CacheCreate != 3 {
		t.Fatalf("result event cache lost: %+v", last)
	}
}

func TestReadTurnNilCallback(t *testing.T) {
	canned := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"x"}}]}}` + "\n" +
		`{"type":"result","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	tr, err := readTurn(context.Background(), startLineReader(strings.NewReader(canned), nil), nil, nil)
	if err != nil {
		t.Fatalf("nil callback must not panic: %v", err)
	}
	if tr.Text != "ok" {
		t.Fatalf("text = %q, want ok", tr.Text)
	}
}

func sameEvent(t *testing.T, got, want contracts.BackendEvent) bool {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encoding event: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encoding event: %v", err)
	}
	return bytes.Equal(gotJSON, wantJSON)
}
