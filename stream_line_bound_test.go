package claude

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

const lineReadSlack = 4096

type repeatBytes struct {
	b    byte
	left int
	read int
}

func (r *repeatBytes) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.left)
	for i := range p[:n] {
		p[i] = r.b
	}
	r.left -= n
	r.read += n
	return n, nil
}

func lineSource(content int) (io.Reader, *repeatBytes) {
	body := &repeatBytes{b: 'a', left: content}
	return io.MultiReader(body, strings.NewReader("\n")), body
}

func TestStartLineReaderRejectsLineOverBound(t *testing.T) {
	source, body := lineSource(maxStreamLineBytes + lineReadSlack)
	done := make(chan struct{})
	defer close(done)
	lines := startLineReader(source, done)
	ln := <-lines
	if !errors.Is(ln.err, ErrStreamLineTooLong) {
		t.Fatalf("err = %v, want ErrStreamLineTooLong", ln.err)
	}
	if body.read > maxStreamLineBytes+lineReadSlack {
		t.Fatalf("read %d bytes of an over-bound line, want at most %d", body.read, maxStreamLineBytes+lineReadSlack)
	}
	if _, open := <-lines; open {
		t.Fatal("the line reader kept going after the over-bound line")
	}
}

func TestStartLineReaderRejectsLineOneByteOverBound(t *testing.T) {
	source, _ := lineSource(maxStreamLineBytes)
	done := make(chan struct{})
	defer close(done)
	ln := <-startLineReader(source, done)
	if !errors.Is(ln.err, ErrStreamLineTooLong) {
		t.Fatalf("err = %v, want ErrStreamLineTooLong for a line of %d bytes with its newline", ln.err, maxStreamLineBytes+1)
	}
}

func TestStartLineReaderAcceptsLineAtBound(t *testing.T) {
	source, _ := lineSource(maxStreamLineBytes - 1)
	done := make(chan struct{})
	defer close(done)
	ln := <-startLineReader(source, done)
	if ln.err != nil {
		t.Fatalf("err = %v, want a line of exactly %d bytes accepted", ln.err, maxStreamLineBytes)
	}
	if len(ln.data) != maxStreamLineBytes {
		t.Fatalf("len = %d, want %d", len(ln.data), maxStreamLineBytes)
	}
}

func TestReadBoundedLineKeepsCapacityWithinBound(t *testing.T) {
	source, _ := lineSource(maxStreamLineBytes - 1)
	data, err := readBoundedLine(bufio.NewReader(source))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cap(data) > maxStreamLineBytes {
		t.Fatalf("cap = %d, want at most %d", cap(data), maxStreamLineBytes)
	}
}

func TestReadTurnReadsFinalLineWithoutNewline(t *testing.T) {
	stream := `{"type":"result","is_error":false,"result":"hello","session_id":"s1"}`
	turn, err := readTurn(context.Background(), startLineReader(strings.NewReader(stream), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "hello" || turn.SessionID != "s1" {
		t.Fatalf("turn = %+v, want text hello in session s1", turn)
	}
}
