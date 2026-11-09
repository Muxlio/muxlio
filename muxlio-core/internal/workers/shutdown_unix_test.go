//go:build unix

package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRemoteShutdownReapsAdapterBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "pid")
	script := testScript(t, "echo $$ > '"+pidPath+"'\nsleep 30 &\nwait\n")
	script.Context = ctx
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "assignments") {
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "n", AttemptID: "a", CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`)})
			return
		}
		_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
	})
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	pidReady := make(chan int, 1)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				data, err := os.ReadFile(pidPath)
				if err == nil {
					pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
					if err == nil {
						pidReady <- pid
						cancel()
						return
					}
				}
			}
		}
	}()
	err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "w", HTTPClient: client}, script)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case pid := <-pidReady:
		if processGroupHasLiveMembers(pid) {
			t.Fatal("adapter process group still has a live process after worker shutdown")
		}
	default:
		t.Fatal("adapter never started")
	}
}

func TestConcurrentAttemptProcessGroupsAndWorkspaces(t *testing.T) {
	script := testScript(t, "pwd > cwd\necho $$ > pid\nsleep 30 &\nwait\n")
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	type child struct {
		ctx  context.Context
		id   string
		done chan error
		pid  int
	}
	children := []child{{ctx: ctxA, id: "a", done: make(chan error, 1)}, {ctx: ctxB, id: "b", done: make(chan error, 1)}}
	for i := range children {
		c := &children[i]
		runner := script.NewAttempt()
		go func() {
			_, err := runner.Execute(ExecuteRequest{Context: c.ctx, RunID: "run", ActivationID: c.id, AttemptID: c.id, Executor: "test"})
			c.done <- err
		}()
	}
	for i := range children {
		c := &children[i]
		dir := filepath.Join(script.workBase, "..", "runs", "run", c.id, c.id)
		attemptDir := dir
		deadline := time.Now().Add(3 * time.Second)
		for {
			matches, _ := filepath.Glob(filepath.Join(attemptDir, "execution-*"))
			if len(matches) == 1 {
				dir = matches[0]
			}
			data, err := os.ReadFile(filepath.Join(dir, "pid"))
			if err == nil {
				c.pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
				if err == nil {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("child failed to start")
			}
			time.Sleep(time.Millisecond)
		}
		data, err := os.ReadFile(filepath.Join(dir, "cwd"))
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(data)) != resolved {
			t.Fatalf("child workspace = %q, want %s", data, resolved)
		}
	}
	if children[0].pid == children[1].pid {
		t.Fatal("children share a process")
	}
	cancelA()
	select {
	case <-children[0].done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled child not reaped")
	}
	if err := syscall.Kill(-children[1].pid, 0); err != nil {
		t.Fatalf("sibling interrupted: %v", err)
	}
	select {
	case <-children[1].done:
		t.Fatal("sibling exited on other child's cancellation")
	default:
	}
	cancelB()
	select {
	case <-children[1].done:
	case <-time.After(3 * time.Second):
		t.Fatal("sibling not reaped")
	}
}
