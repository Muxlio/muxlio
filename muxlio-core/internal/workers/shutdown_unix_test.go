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
		if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
			t.Fatalf("adapter process group still exists after worker shutdown: %v", err)
		}
	default:
		t.Fatal("adapter never started")
	}
}
