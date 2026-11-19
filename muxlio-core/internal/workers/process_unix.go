//go:build unix

package workers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
		reapProcessGroup(cmd)
		return startedAt, ctx.Err()
	}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func reapProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processGroupHasLiveMembers(pgid) {
			return
		}
		killProcessGroup(cmd)
		time.Sleep(50 * time.Millisecond)
	}
}

func processGroupHasLiveMembers(pgid int) bool {
	if runtime.GOOS != "linux" {
		return syscall.Kill(-pgid, 0) == nil
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return syscall.Kill(-pgid, 0) == nil
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		// The command name is parenthesized and may itself contain spaces.
		statText := string(stat)
		fields := strings.Fields(statText[strings.LastIndexByte(statText, ')')+1:])
		if len(fields) < 3 || fields[0] == "Z" || fields[0] == "X" {
			continue
		}
		group, err := strconv.Atoi(fields[2])
		if err == nil && group == pgid {
			return true
		}
	}
	return false
}
