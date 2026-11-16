//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessCleansDescendants(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "leader exits"
		if cancelled {
			name = "leader exits on cancellation"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ready, leaked := filepath.Join(dir, "ready"), filepath.Join(dir, "leaked")
			// The child ignores TERM and closes output descriptors. The leader
			// can therefore exit while the child still owns external work.
			script := `sh -c 'trap "" TERM; echo ready > "$1"; sleep 0.3; echo leaked > "$2"' child "$1" "$2" >/dev/null 2>&1 &
while [ ! -f "$1" ]; do sleep 0.01; done
`
			if cancelled {
				script += "wait\n"
			}
			cmd := exec.Command("/bin/sh", "-c", script, "leader", ready, leaked)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runCommand(ctx, cmd) }()
			if cancelled {
				deadline := time.Now().Add(2 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("descendant did not start")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error = %v", err)
				}
				if !cancelled && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("process did not return")
			}
			time.Sleep(500 * time.Millisecond)
			if _, err := os.Stat(leaked); !os.IsNotExist(err) {
				t.Fatalf("descendant continued after return: %v", err)
			}
		})
	}
}

func TestLeaderExitKillsDescendantsHoldingOutput(t *testing.T) {
	dir := t.TempDir()
	leaked := filepath.Join(dir, "leaked")
	// The descendant keeps the captured stdout and stderr pipes open.
	script := `sh -c 'echo started; sleep 2; echo leaked > "$1"' child "$1" &
echo leader
`
	cmd := exec.Command("/bin/sh", "-c", script, "leader", leaked)
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out)
	cmd.Stderr = io.MultiWriter(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := runCommand(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("leader exit waited %s for descendant output", elapsed)
	}
	if !strings.Contains(out.String(), "leader") {
		t.Fatalf("leader output lost: %q", out.String())
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Fatalf("descendant continued after return: %v", err)
	}
}
