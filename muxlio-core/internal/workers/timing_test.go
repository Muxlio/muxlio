package workers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
