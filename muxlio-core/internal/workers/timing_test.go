package workers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
)

type timingExecutor struct{ err error }

func (x timingExecutor) Execute(ExecuteRequest) (ExecuteResult, error) {
	time.Sleep(10 * time.Millisecond)
	return ExecuteResult{}, x.err
}

func TestRemoteTimingExcludesDispatchAndCompletionDelays(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var completion remoteCompletion
			var executionErr error
			if failed {
				executionErr = errors.New("execution failed")
			}
			client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var body any
				switch {
				case strings.HasSuffix(r.URL.Path, "register"):
					body = remoteRegistrationResult{CoordinatorEpoch: 1}
				case strings.HasSuffix(r.URL.Path, "heartbeat"):
					body = remoteHeartbeatResult{CoordinatorEpoch: 1}
				case strings.HasSuffix(r.URL.Path, "assignments"):
					// Deliberately spend much longer in dispatch than in executor execution.
					time.Sleep(150 * time.Millisecond)
					body = remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`)}
				case strings.HasSuffix(r.URL.Path, "complete"):
					if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
						t.Error(err)
					}
					time.Sleep(150 * time.Millisecond)
					cancel()
				}
				raw, _ := json.Marshal(body)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			if err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client, HeartbeatInterval: time.Second, WorkBase: t.TempDir()}, timingExecutor{err: executionErr}); err != nil {
				t.Fatal(err)
			}
			// Millisecond durations truncate, so allow timer granularity around the
			// executor's 10 ms sleep while still rejecting missing or dispatch time.
			if completion.ExecutionDurationMS == nil || *completion.ExecutionDurationMS < 5 || *completion.ExecutionDurationMS >= 150 {
				t.Fatalf("worker timing includes queue/completion delay: %+v", completion)
			}
			if (completion.State == "failed") != failed {
				t.Fatalf("unexpected completion: %+v", completion)
			}
		})
	}
}

func TestRemoteTimingExcludesArtifactTransferDelays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := []byte("payload")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	req, _ := json.Marshal(map[string]any{
		"executor": "test",
		"inputs": map[string]any{
			"input": map[string]any{"digest": digest, "size": float64(len(payload)), "path": "input.txt"},
		},
	})
	var completion remoteCompletion
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case strings.Contains(r.URL.Path, "/artifacts/") && strings.HasSuffix(r.URL.Path, "/transfer"):
			time.Sleep(200 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: digest, URL: "/bytes", Method: http.MethodGet, Mode: "proxy"})
		case r.URL.Path == "/bytes":
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "assignments"):
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, Request: req})
		case strings.HasSuffix(r.URL.Path, "complete"):
			if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
				t.Error(err)
			}
			cancel()
		default:
			t.Errorf("unexpected request %s", r.URL.String())
		}
		return w.Result(), nil
	})}
	if err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client, HeartbeatInterval: time.Second, WorkBase: t.TempDir()}, timingExecutor{}); err != nil {
		t.Fatal(err)
	}
	if completion.State != "succeeded" {
		t.Fatalf("unexpected completion state=%s err=%+v", completion.State, completion.Error)
	}
	if completion.ExecutionDurationMS == nil || *completion.ExecutionDurationMS < 5 || *completion.ExecutionDurationMS >= 150 {
		t.Fatalf("worker timing includes artifact transfer: %+v", completion)
	}
}

func TestQueueTimeoutErrorIsDistinctFromExecutionDeadline(t *testing.T) {
	err := QueueTimeoutError{Deadline: time.Now()}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queue timeout should unwrap deadline exceeded for retry policy")
	}
	var qt QueueTimeoutError
	if !errors.As(err, &qt) {
		t.Fatal("errors.As should identify queue timeout")
	}
}

func TestLocalScriptFailureHasWorkerTiming(t *testing.T) {
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	result, err := s.Execute(ExecuteRequest{Executor: "missing"})
	if err == nil && result.Error == "" {
		t.Fatal("expected missing adapter failure")
	}
	if result.ExecutionDurationMS == nil || *result.ExecutionDurationMS < 0 {
		t.Fatalf("local failure timing missing: %+v", result)
	}
}
