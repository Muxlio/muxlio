package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type assignmentNumberEcho struct{ values chan string }

func (e assignmentNumberEcho) Execute(req ExecuteRequest) (ExecuteResult, error) {
	raw, _ := json.Marshal(req.Inputs["value"])
	e.values <- string(raw)
	return ExecuteResult{Outputs: map[string]any{"value": req.Inputs["value"]}}, nil
}
func TestRemoteAssignmentExactNumbers(t *testing.T) {
	for _, number := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(number, func(t *testing.T) {
			mapped := []byte(number)
			raw, _ := json.Marshal(ExecuteRequest{Executor: "test/echo", Inputs: map[string]any{"value": json.RawMessage(mapped)}})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var polled atomic.Bool
			completed := make(chan json.RawMessage, 1)
			values := make(chan string, 1)
			client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch {
				case strings.HasSuffix(req.URL.Path, "register"):
					json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
				case strings.HasSuffix(req.URL.Path, "assignments"):
					if polled.Swap(true) {
						w.WriteHeader(204)
					} else {
						json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "slot", RunID: "run", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, Request: raw, LeaseUntil: time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
					}
				case strings.HasSuffix(req.URL.Path, "heartbeat"):
					json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30})
				case strings.HasSuffix(req.URL.Path, "complete"):
					var v json.RawMessage
					json.NewDecoder(req.Body).Decode(&v)
					completed <- v
					w.WriteHeader(202)
				default:
					w.WriteHeader(202)
				}
				return w.Result(), nil
			})}
			stopped := make(chan error, 1)
			go func() {
				stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://isolated", WorkerID: "worker", Capacity: 1, HTTPClient: client, PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond, WorkBase: t.TempDir()}, assignmentNumberEcho{values})
			}()
			select {
			case c := <-completed:
				var completion struct{ Outputs map[string]json.RawMessage }
				if err := json.Unmarshal(c, &completion); err != nil {
					t.Fatal(err)
				}
				if string(completion.Outputs["value"]) != number {
					t.Errorf("completion=%s", c)
				}
				select {
				case v := <-values:
					t.Logf("mapped=%s executor=%s completion=%s", mapped, v, c)
					if v != number {
						t.Errorf("numeric corruption: got %s want %s", v, number)
					}
				default:
					t.Errorf("executor never called; completion=%s", c)
				}
			case <-ctx.Done():
				t.Fatal("timeout")
			}
			cancel()
			<-stopped
		})
	}
}
