package claude

import (
	"context"
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
