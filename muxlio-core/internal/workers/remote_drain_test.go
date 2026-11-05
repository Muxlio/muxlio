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

type drainExecutor struct {
	started chan ExecuteRequest
	release chan struct{}
}

func (e *drainExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	select {
	case e.started <- req:
	case <-req.Context.Done():
		return ExecuteResult{}, req.Context.Err()
	}
	select {
	case <-e.release:
		return ExecuteResult{Outputs: map[string]any{"finished": true}}, nil
	case <-req.Context.Done():
		return ExecuteResult{}, req.Context.Err()
	}
}

func TestRemoteWorkerDrainAndGracefulShutdown(t *testing.T) {
	for _, mode := range []string{"drain", "shutdown", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drain := make(chan struct{})
			e := &drainExecutor{started: make(chan ExecuteRequest, 1), release: make(chan struct{})}
			heartbeats := make(chan remoteHeartbeat, 100)
			completions := make(chan remoteCompletion, 1)
			var polls atomic.Int32
			client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch {
				case strings.HasSuffix(req.URL.Path, "register"):
					_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
				case strings.HasSuffix(req.URL.Path, "heartbeat"):
					var body remoteHeartbeat
					_ = json.NewDecoder(req.Body).Decode(&body)
					select {
					case heartbeats <- body:
					default:
					}
					_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30})
				case strings.HasSuffix(req.URL.Path, "assignments"):
					polls.Add(1)
					_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "slot", RunID: "run", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 7, LeaseUntil: time.Now().Add(time.Minute).Format(time.RFC3339Nano), Request: json.RawMessage(`{"executor":"test"}`)})
				case strings.HasSuffix(req.URL.Path, "complete"):
					var body remoteCompletion
					_ = json.NewDecoder(req.Body).Decode(&body)
					completions <- body
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
				}
				return w.Result(), nil
			})}
			grace := time.Second
			if mode == "deadline" {
				grace = 30 * time.Millisecond
			}
			stopped := make(chan error, 1)
			go func() {
				stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: 1, HTTPClient: client, Drain: drain, ShutdownGracePeriod: grace, HeartbeatInterval: time.Millisecond}, e)
			}()
			watchdog := time.NewTimer(3 * time.Second)
			defer watchdog.Stop()
			var child ExecuteRequest
			select {
			case child = <-e.started:
			case <-watchdog.C:
				t.Fatal("attempt never started")
			}
			if mode == "drain" {
				close(drain)
			} else {
				cancel()
			}
			for {
				select {
				case hb := <-heartbeats:
					if !hb.Draining {
						continue
					}
					if hb.Capacity == nil || *hb.Capacity != 0 || hb.AvailableSlots != 0 || len(hb.Leases) != 1 || hb.Leases[0].Generation != 7 || hb.Leases[0].CoordinatorEpoch != 1 {
						t.Fatalf("invalid drain heartbeat: %+v", hb)
					}
				case <-watchdog.C:
					t.Fatal("drain never announced")
				}
				break
			}
			if mode != "deadline" {
				if child.Context.Err() != nil {
					t.Fatal("drain cancelled admitted work")
				}
				close(e.release)
				select {
				case c := <-completions:
					if c.State != "succeeded" || c.Outputs["finished"] != true {
						t.Fatalf("completion lost: %+v", c)
					}
				case <-watchdog.C:
					t.Fatal("completion never delivered")
				}
			}
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-watchdog.C:
				t.Fatal("worker did not finish draining")
			}
			if polls.Load() != 1 {
				t.Fatalf("draining worker polled %d times", polls.Load())
			}
			if mode == "deadline" && child.Context.Err() == nil {
				t.Fatal("shutdown deadline left execution running")
			}
		})
	}
}

func TestRemoteWorkerCancelsExpiredSlotDuringBlockedHeartbeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e := &drainExecutor{started: make(chan ExecuteRequest, 1), release: make(chan struct{})}
	var calls atomic.Int32
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(req.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "heartbeat"):
			if calls.Add(1) > 1 {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "assignments"):
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "slot", RunID: "run", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, LeaseUntil: time.Now().Add(30 * time.Millisecond).Format(time.RFC3339Nano), Request: json.RawMessage(`{"executor":"test"}`)})
		case strings.HasSuffix(req.URL.Path, "complete"):
			w.WriteHeader(http.StatusConflict)
		}
		return w.Result(), nil
	})}
	stopped := make(chan error, 1)
	go func() {
		stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client, HeartbeatInterval: time.Millisecond}, e)
	}()
	defer func() { cancel(); <-stopped }()
	select {
	case req := <-e.started:
		select {
		case <-req.Context.Done():
		case <-time.After(time.Second):
			t.Fatal("blocked heartbeat prevented local lease cancellation")
		}
	case <-ctx.Done():
		t.Fatal("attempt did not start")
	}
}
