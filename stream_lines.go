package claude

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

type readLine struct {
	data []byte
	err  error
}

const maxStreamLineBytes = 16 << 20

const lineReadBufferBytes = 4 << 10

// ErrStreamLineTooLong matches a stream line that does not end within the size limit.
// The turn is not retried: the CLI may already have acted on the lines before it.
var ErrStreamLineTooLong = errors.New("claude stream line exceeds the size limit")

func readBoundedLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maxStreamLineBytes {
			return nil, fmt.Errorf("%w: no line end within %d bytes", ErrStreamLineTooLong, maxStreamLineBytes)
		}
		line = appendWithinBound(line, chunk)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

const lineGrowthFactor = 2

func appendWithinBound(line, chunk []byte) []byte {
	need := len(line) + len(chunk)
	if need > cap(line) {
		grown := make([]byte, len(line), min(max(lineGrowthFactor*cap(line), need), maxStreamLineBytes))
		copy(grown, line)
		line = grown
	}
	return append(line, chunk...)
}

func startLineReader(source io.Reader, done <-chan struct{}) chan readLine {
	lines := make(chan readLine)
	go func() {
		defer close(lines)
		reader := bufio.NewReaderSize(source, lineReadBufferBytes)
		for {
			data, err := readBoundedLine(reader)
			select {
			case lines <- readLine{data, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return lines
}
