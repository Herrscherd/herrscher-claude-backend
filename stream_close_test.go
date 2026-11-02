package claude

import (
	"os/exec"
	"testing"
)

func startTestSession(t *testing.T, name string, args ...string) *streamSession {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	s := newStreamSession(stdin, stdout)
	s.cmd = cmd
	return s
}

func TestStreamSessionCloseTwiceReturnsNil(t *testing.T) {
	t.Parallel()
	s := startTestSession(t, "sh", "-c", "sleep 60")
	if err := s.Close(); err != nil {
		t.Fatalf("first Close returned %v, want nil after a kill", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close returned %v, want nil", err)
	}
}

func TestStreamSessionCloseAfterNormalExitReturnsNil(t *testing.T) {
	t.Parallel()
	s := startTestSession(t, "sh", "-c", "exit 0")
	for range s.lines {
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after a normal exit returned %v, want nil", err)
	}
}
