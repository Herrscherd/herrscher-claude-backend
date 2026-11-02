package claude

import (
	"context"
	"strings"
	"testing"
)

func TestReadTurnSkipsMalformedLines(t *testing.T) {
	stream := "not json at all\n" +
		`{"type":"result","is_error":false,"result":"hello","session_id":"s1"}` + "\n"
	lines := startLineReader(strings.NewReader(stream))
	turn, err := readTurn(context.Background(), lines, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "hello" || turn.SessionID != "s1" {
		t.Fatalf("turn = %+v, want text hello in session s1", turn)
	}
}
