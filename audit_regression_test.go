package claude

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Herrscherd/herrscher-contracts"
)

func TestStreamSessionConsecutiveTurns(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go func() {
		br := bufio.NewReader(stdinR)
		for _, reply := range []string{"first", "second", "third"} {
			if _, err := br.ReadBytes('\n'); err != nil {
				return
			}
			io.WriteString(stdoutW,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"`+reply+`"}]}}`+"\n"+
					`{"type":"result","is_error":false,"result":"`+reply+`","session_id":"s-`+reply+`"}`+"\n")
		}
		stdoutW.Close()
	}()

	s := newStreamSession(stdinW, stdoutR)
	for _, tc := range []struct {
		name string
		want string
	}{
		{"turn1", "first"},
		{"turn2", "second"},
		{"turn3", "third"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			tr, err := s.Send(ctx, "hi", nil)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if tr.Text != tc.want {
				t.Fatalf("%s: text = %q, want %q", tc.name, tr.Text, tc.want)
			}
			if tr.SessionID != "s-"+tc.want {
				t.Fatalf("%s: session id = %q", tc.name, tr.SessionID)
			}
		})
	}
}

func TestParseTurnLineResultDecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		line    string
		wantErr bool
		done    bool
		text    string
	}{
		{"valid", `{"type":"result","is_error":false,"result":"ok","session_id":"s"}`, false, true, "ok"},
		{"field type drift", `{"type":"result","is_error":false,"result":"ok","total_cost_usd":"0.0123","session_id":"s"}`, false, true, "ok"},
		{"nested type drift", `{"type":"result","is_error":false,"result":"ok","usage":7,"session_id":"s"}`, false, true, "ok"},
		{"not json", `boot: starting claude`, false, false, ""},
		{"unknown type", `{"type":"system","subtype":"init"}`, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, done, err := newTurnParser().parseLine([]byte(tc.line), nil)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if done != tc.done {
				t.Fatalf("done = %v, want %v", done, tc.done)
			}
			if tr.Text != tc.text {
				t.Fatalf("text = %q, want %q", tr.Text, tc.text)
			}
		})
	}
}

func TestReadTurnDoesNotHangOnDriftedResult(t *testing.T) {
	canned := `{"type":"result","is_error":false,"result":"ok","total_cost_usd":"0.0123","session_id":"s"}` + "\n"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tr, err := readTurn(ctx, startLineReader(strings.NewReader(canned)), nil)
	if err != nil {
		t.Fatalf("drifted result line must still terminate the turn: %v", err)
	}
	if tr.Text != "ok" || tr.SessionID != "s" {
		t.Fatalf("turn result lost usable fields: %+v", tr)
	}
}

func TestRespondFailedRestartClearsSession(t *testing.T) {
	dead := newStreamSession(discardWriteCloser{}, strings.NewReader(""))
	dead.sessID = "conv-7"
	r := &streamResponder{ctx: context.Background(), base: []string{"herrscher-no-such-binary"}, sess: dead}

	if _, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi"}, nil); err == nil {
		t.Fatal("expected the failed restart to error")
	}
	if r.sess != nil {
		t.Fatal("a failed restart must not leave a dead session installed")
	}
	if got := r.ResumeToken(); got != "conv-7" {
		t.Fatalf("resume token = %q, want conv-7", got)
	}
}

func TestErrFromTurnIsSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		tr   turnResult
		msg  string
	}{
		{"model message", turnResult{IsError: true, ErrMsg: "rate limit"}, "rate limit"},
		{"no message", turnResult{IsError: true}, "claude reported an error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := errFromTurn(tc.tr, "s-1")
			if !errors.Is(err, ErrTurnFailed) {
				t.Fatalf("errors.Is(ErrTurnFailed) = false for %v", err)
			}
			if err.Error() != tc.msg {
				t.Fatalf("message = %q, want %q", err.Error(), tc.msg)
			}
			var te *turnError
			if !errors.As(err, &te) || te.SessionID() != "s-1" {
				t.Fatalf("session id not carried: %+v", te)
			}
		})
	}
}

func TestResumeTokenDoesNotBlockDuringTurn(t *testing.T) {
	pr, pw := io.Pipe()
	sess := newStreamSession(discardWriteCloser{}, pr)
	r := &streamResponder{ctx: context.Background(), resumeID: "boot", sess: sess}

	turnDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = r.Respond(ctx, contracts.Prompt{Content: "hi"}, nil)
		close(turnDone)
	}()

	got := make(chan string, 1)
	go func() { got <- r.ResumeToken() }()
	select {
	case tok := <-got:
		if tok != "boot" {
			t.Errorf("token = %q, want boot", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResumeToken blocked behind an in-flight turn")
	}
	cancel()
	_ = pw.Close()
	<-turnDone
}
