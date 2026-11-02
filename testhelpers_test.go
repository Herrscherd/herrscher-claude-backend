package claude

import (
	"io"
	"testing"
)

func mustWriteString(t *testing.T, writer io.Writer, text string) {
	t.Helper()
	if _, err := io.WriteString(writer, text); err != nil {
		t.Errorf("write: %v", err)
	}
}

func mustClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
