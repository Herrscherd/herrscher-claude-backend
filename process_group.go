package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const groupWaitDelay = time.Second

func newGroupCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = groupWaitDelay
	cmd.Cancel = func() error { return killGroup(cmd) }
	detachIntoGroup(cmd)
	return cmd
}

func runGroup(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	waitErr := cmd.Wait()
	if errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		waitErr = nil
	}
	killErr := killGroup(cmd)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	return errors.Join(waitErr, killErr)
}
