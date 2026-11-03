//go:build unix

package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return ctx.Err()
	}
}
