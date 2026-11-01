package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	sendCancel := false
	polls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			var hb remoteHeartbeatResult
			if sendCancel {
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
			sendCancel = true
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
	newEpoch := false
	polls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 2})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			epoch := int64(1)
			if newEpoch {
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
			newEpoch = true
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
