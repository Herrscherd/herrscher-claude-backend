//go:build windows

package claude

import "os/exec"

func detachIntoGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
