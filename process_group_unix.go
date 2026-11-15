//go:build unix

package claude

import (
	"errors"
	"os/exec"
	"syscall"
)

func detachIntoGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return cmd.Process.Kill()
	}
	return err
}
