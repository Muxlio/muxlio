//go:build unix

package workers

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

const openNonblock = syscall.O_NONBLOCK

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
}

// runProcess starts cmd and returns the wall time of a successful Start.
// A zero startedAt means the process never launched.
func runProcess(ctx context.Context, cmd *exec.Cmd) (startedAt time.Time, err error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if err := cmd.Start(); err != nil {
		return time.Time{}, err
	}
	startedAt = time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// Reaping the leader does not imply that its descendants have exited.
		killProcessGroup(cmd)
		return startedAt, err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		select {
		case <-done:
			killProcessGroup(cmd)
		case <-time.After(500 * time.Millisecond):
			killProcessGroup(cmd)
			<-done
		}
		return startedAt, ctx.Err()
	}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
