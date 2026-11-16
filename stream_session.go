package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type streamSession struct {
	mu          sync.Mutex
	stateMu     sync.Mutex
	stdin       io.WriteCloser
	lines       chan readLine
	done        chan struct{}
	stopOnce    sync.Once
	cmd         *exec.Cmd
	sessID      string
	resumedFrom string
}

func newStreamSession(stdin io.WriteCloser, out io.Reader) *streamSession {
	done := make(chan struct{})
	return &streamSession{stdin: stdin, lines: startLineReader(out, done), done: done}
}

func streamArgv(base []string, model, resumeID string) []string {
	argv := append([]string{}, base...)
	argv = append(argv, "-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose")
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if resumeID != "" {
		argv = append(argv, "--resume", resumeID)
	}
	return argv
}

func startStreamSession(ctx context.Context, base []string, model, resumeID, dir string, env map[string]string) (*streamSession, error) {
	argv := streamArgv(base, model, resumeID)
	cmd := newGroupCommand(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = spawnEnv(os.Environ(), env)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stream: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stream: stdout pipe: %w", err)
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude stream: start: %w", err)
	}
	session := newStreamSession(stdin, stdout)
	session.cmd = cmd
	session.sessID = resumeID
	session.resumedFrom = resumeID
	return session, nil
}

func (s *streamSession) Send(ctx context.Context, text string, onEvent func(contracts.BackendEvent), onSessionID func(string)) (turnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := userLine(text)
	if err != nil {
		return turnResult{}, err
	}
	if _, err = s.writeStdin(line); err != nil {
		return turnResult{}, err
	}
	turn, err := readTurn(ctx, s.lines, s.done, onEvent, onSessionID)
	if turn.SessionID != "" {
		s.sessID = turn.SessionID
	}
	return turn, err
}

func (s *streamSession) writeStdin(line []byte) (int, error) {
	s.stateMu.Lock()
	stdin := s.stdin
	s.stateMu.Unlock()
	if stdin == nil {
		return 0, io.ErrClosedPipe
	}
	return stdin.Write(line)
}

func (s *streamSession) Close() error {
	s.stopOnce.Do(func() { close(s.done) })
	s.stateMu.Lock()
	stdin, cmd := s.stdin, s.cmd
	s.stdin, s.cmd = nil, nil
	s.stateMu.Unlock()
	var closeErrs []error
	if stdin != nil {
		closeErrs = append(closeErrs, stdin.Close())
	}
	if cmd != nil && cmd.Process != nil {
		closeErrs = append(closeErrs, killAndReap(cmd))
	}
	return errors.Join(closeErrs...)
}

func killAndReap(cmd *exec.Cmd) error {
	killErr := killGroup(cmd)
	killed := killErr == nil
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waitErr := cmd.Wait()
	if _, isExitErr := errors.AsType[*exec.ExitError](waitErr); isExitErr && killed {
		waitErr = nil
	}
	return errors.Join(killErr, waitErr)
}
