//go:build unix

package workers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
			configureProcess(cmd)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runProcess(ctx, cmd) }()
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
