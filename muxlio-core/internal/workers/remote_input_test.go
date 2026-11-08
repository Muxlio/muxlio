package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

type drainingInputExecutor struct {
	ctx         context.Context
	liveStarted chan struct{}
	releaseTurn chan struct{}
	releaseLive chan struct{}
}

func (e *drainingInputExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	if req.Action == "steer" {
		close(e.liveStarted)
		// Model an acknowledgment arriving after the active turn completes.
		select {
		case <-e.releaseLive:
			return ExecuteResult{AcknowledgedInputs: []string{"input"}}, nil
		case <-e.ctx.Done():
			return ExecuteResult{}, e.ctx.Err()
		}
	}
	select {
	case <-e.releaseTurn:
		return ExecuteResult{}, nil
	case <-req.Context.Done():
		return ExecuteResult{}, req.Context.Err()
	}
}

func TestRemoteWorkerDrainFlushesLiveInputReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	drain := make(chan struct{})
	e := &drainingInputExecutor{ctx: ctx, liveStarted: make(chan struct{}), releaseTurn: make(chan struct{}), releaseLive: make(chan struct{})}
	draining := make(chan struct{}, 1)
	var releaseLive sync.Once
	var polled, offered atomic.Bool
	var receiptAttempts atomic.Int32
	handle := &contract.SessionHandle{ID: "session", Executor: "test", RunnerVersion: "1", OwnerWorker: "worker", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}
	raw, err := json.Marshal(ExecuteRequest{Executor: "test", Inputs: map[string]any{"number": json.Number("1e400")}, Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(req.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "assignments"):
			if polled.Swap(true) {
				w.WriteHeader(http.StatusNoContent)
				break
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "slot", RunID: "run", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, LeaseUntil: time.Now().Add(time.Minute).Format(time.RFC3339Nano), Request: raw})
		case strings.HasSuffix(req.URL.Path, "heartbeat"):
			var hb remoteHeartbeat
			_ = json.NewDecoder(req.Body).Decode(&hb)
			response := remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30}
			if len(hb.Active) > 0 && !offered.Swap(true) {
				command, _ := json.Marshal(map[string]any{"sessionInput": map[string]any{"assignmentId": "slot", "inputs": []contract.SessionInput{{Version: "1", RequestID: "input", Message: "direction"}}}})
				response.Commands = []json.RawMessage{command}
			}
			if hb.Draining {
				select {
				case draining <- struct{}{}:
				default:
				}
				if len(hb.Active) == 0 {
					releaseLive.Do(func() { close(e.releaseLive) })
				}
			}
			if len(hb.InputReceipts) > 0 {
				if receiptAttempts.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					break
				}
			}
			_ = json.NewEncoder(w).Encode(response)
		case strings.HasSuffix(req.URL.Path, "complete"):
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
		}
		return w.Result(), nil
	})}
	stopped := make(chan error, 1)
	go func() {
		stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: 1, HTTPClient: client, Drain: drain, HeartbeatInterval: time.Millisecond, WorkBase: t.TempDir()}, e)
	}()
	select {
	case <-e.liveStarted:
	case <-ctx.Done():
		t.Fatal("live delivery did not start")
	}
	close(drain)
	select {
	case <-draining:
	case <-ctx.Done():
		t.Fatal("worker did not announce drain")
	}
	close(e.releaseTurn)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not flush receipts and drain")
	}
	if receiptAttempts.Load() < 2 {
		t.Fatalf("worker exited without retrying the live input receipt: %d attempts", receiptAttempts.Load())
	}
}
