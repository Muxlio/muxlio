//go:build !unix

package workers

import (
	"context"
	"os/exec"
	"time"
)

const openNonblock = 0

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = time.Second }

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
		return startedAt, err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return startedAt, ctx.Err()
	}
}
