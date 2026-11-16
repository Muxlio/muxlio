//go:build unix

package main

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// Descendants that inherit stdout/stderr would otherwise keep Wait copying
// until they exit; the delay lets Wait observe the leader's exit and the group
// kill below then ends them.
const descendantOutputDelay = 100 * time.Millisecond

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = descendantOutputDelay
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
			return ctx.Err()
		}
	}
}
