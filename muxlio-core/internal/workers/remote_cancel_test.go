package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingExecutor struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockingExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	b.once.Do(func() { close(b.started) })
	<-req.Context.Done()
	return ExecuteResult{Error: "assignment revoked"}, req.Context.Err()
}

func TestRemoteWorkerCancelDuringExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	block := &blockingExecutor{started: make(chan struct{})}
	var sendCancel atomic.Bool
	polls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			var hb remoteHeartbeatResult
			if sendCancel.Load() {
				raw, _ := json.Marshal(map[string]any{"cancel": map[string]any{"assignmentId": "assignment"}})
				hb = remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30, Commands: []json.RawMessage{raw}}
			} else {
				hb = remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30}
			}
			_ = json.NewEncoder(w).Encode(hb)
		case strings.HasSuffix(r.URL.Path, "assignments") && r.Method == http.MethodGet:
			polls++
			if polls > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{
				AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt",
				CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`),
			})
		case strings.HasSuffix(r.URL.Path, "complete"):
			var completion remoteCompletion
			_ = json.NewDecoder(r.Body).Decode(&completion)
			if completion.State != "cancelled" {
				t.Errorf("completion state = %q", completion.State)
			}
			w.WriteHeader(http.StatusAccepted)
			cancel()
		}
	})
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Body = io.NopCloser(strings.NewReader(recorder.Body.String()))
		return response, nil
	})}
	go func() {
		select {
		case <-block.started:
			sendCancel.Store(true)
		case <-ctx.Done():
		}
	}()
	err := RunRemoteWorker(ctx, RemoteWorkerConfig{
		CoordinatorURL:    "http://coordinator",
		HTTPClient:        client,
		WorkerID:          "worker",
		PollInterval:      time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	}, block)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkerStaleEpochHeartbeatRevokesAssignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	block := &blockingExecutor{started: make(chan struct{})}
	var newEpoch atomic.Bool
	polls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 2})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			epoch := int64(1)
			if newEpoch.Load() {
				epoch = 2
			}
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: epoch, LeaseSeconds: 30})
		case strings.HasSuffix(r.URL.Path, "assignments") && r.Method == http.MethodGet:
			polls++
			if polls > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{
				AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt",
				CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`),
			})
		case strings.HasSuffix(r.URL.Path, "complete"):
			var completion remoteCompletion
			_ = json.NewDecoder(r.Body).Decode(&completion)
			if completion.State != "cancelled" || completion.CoordinatorEpoch != 1 {
				t.Errorf("completion = %+v", completion)
			}
			w.WriteHeader(http.StatusAccepted)
			cancel()
		}
	})
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Body = io.NopCloser(strings.NewReader(recorder.Body.String()))
		return response, nil
	})}
	go func() {
		select {
		case <-block.started:
			newEpoch.Store(true)
		case <-ctx.Done():
		}
	}()
	err := RunRemoteWorker(ctx, RemoteWorkerConfig{
		CoordinatorURL:    "http://coordinator",
		HTTPClient:        client,
		WorkerID:          "worker",
		PollInterval:      time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	}, block)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkerDoesNotInvokeExpiredAssignment(t *testing.T) {
	for _, field := range []string{"deadline", "executionDeadline"} {
		t.Run(field, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			var completed atomic.Bool
			past := time.Now().Add(-time.Second)
			raw, _ := json.Marshal(map[string]any{"executor": "test", field: past})
			var polls atomic.Int32
			client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var body any
				status := http.StatusOK
				switch {
				case strings.HasSuffix(r.URL.Path, "register"):
					body = remoteRegistrationResult{CoordinatorEpoch: 1}
				case strings.HasSuffix(r.URL.Path, "heartbeat"):
					body = remoteHeartbeatResult{CoordinatorEpoch: 1}
				case strings.HasSuffix(r.URL.Path, "assignments"):
					if polls.Add(1) == 1 {
						body = remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, Request: raw}
					} else {
						status = http.StatusNoContent
					}
				case strings.HasSuffix(r.URL.Path, "complete"):
					var completion remoteCompletion
					if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
						t.Error(err)
					}
					if completion.State == "succeeded" || len(completion.Outputs) != 0 {
						t.Errorf("expired completion=%+v", completion)
					}
					completed.Store(true)
					cancel()
				}
				encoded, _ := json.Marshal(body)
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
			})}
			executor := cancellationTestExecutor(func(ExecuteRequest) (ExecuteResult, error) {
				calls.Add(1)
				return ExecuteResult{Outputs: map[string]any{"late": true}}, nil
			})
			if err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client, PollInterval: time.Millisecond, WorkBase: t.TempDir()}, executor); err != nil {
				t.Fatal(err)
			}
			if !completed.Load() || calls.Load() != 0 {
				t.Fatalf("completed=%v executor calls=%d", completed.Load(), calls.Load())
			}
		})
	}
}
