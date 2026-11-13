//go:build unix

package authoring

import (
	"os/exec"
	"syscall"
)

func boundProcess(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// The leader can exit before its descendants. CommandContext stops
	// watching once Wait returns, and WaitDelay only closes inherited pipes;
	// neither cleans up those remaining processes on normal exit or failure.
	return func() {
		if cmd.Process != nil {
			_ = cmd.Cancel()
		}
	}
}
