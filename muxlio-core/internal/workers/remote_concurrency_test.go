package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type attemptRunner struct {
	started chan<- ExecuteRequest
	release []chan struct{}
	used    atomic.Bool
}

func (r *attemptRunner) Execute(req ExecuteRequest) (ExecuteResult, error) {
	if r.used.Swap(true) {
		return ExecuteResult{}, fmt.Errorf("runner reused")
	}
	var i int
	_, _ = fmt.Sscanf(req.AttemptID, "attempt-%d", &i)
	r.started <- req
	if err := req.LogSink.Append("agent", []byte(req.AttemptID)); err != nil {
		return ExecuteResult{}, err
	}
	select {
	case <-r.release[i]:
		return ExecuteResult{Outputs: map[string]any{"id": req.AttemptID}}, nil
	case <-req.Context.Done():
		return ExecuteResult{}, req.Context.Err()
	}
}

// Uses generic executor fixtures: every child has its own runner, context,
// identity and log sequence while the supervisor enforces a changing limit.
func TestRemoteWorkerConcurrentAdmissionAndChildCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan ExecuteRequest, 6)
	release := make([]chan struct{}, 6)
	for i := range release {
		release[i] = make(chan struct{})
	}
	completions := make(chan remoteCompletion, 6)
	heartbeats := make(chan remoteHeartbeat, 100)
	var limit atomic.Int32
	limit.Store(4)
	var revoke atomic.Bool
	var next atomic.Int32
	var factories atomic.Int32
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
			hb := remoteHeartbeatResult{CoordinatorEpoch: 1, LeaseSeconds: 30}
			if revoke.Load() {
				hb.Commands = []json.RawMessage{json.RawMessage(`{"cancel":{"assignmentId":"assignment-0"}}`)}
			}
			_ = json.NewEncoder(w).Encode(hb)
		case strings.HasSuffix(req.URL.Path, "assignments"):
			i := int(next.Add(1)) - 1
			if i >= 6 {
				w.WriteHeader(http.StatusNoContent)
				break
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{
				AssignmentID: fmt.Sprintf("assignment-%d", i), RunID: "run", ActivationID: fmt.Sprintf("activation-%d", i), AttemptID: fmt.Sprintf("attempt-%d", i),
				CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(fmt.Sprintf(`{"executor":"test/%d","attemptId":"untrusted"}`, i%2)),
			})
		case strings.HasSuffix(req.URL.Path, "/log"):
			var log struct {
				AssignmentID string
				AttemptID    string
				Seq          int
				Data         []byte
			}
			_ = json.NewDecoder(req.Body).Decode(&log)
			if log.Seq != 1 || string(log.Data) != log.AttemptID {
				t.Errorf("crossed log stream: %+v", log)
			}
			w.WriteHeader(http.StatusAccepted)
		case strings.HasSuffix(req.URL.Path, "complete"):
			var completion remoteCompletion
			_ = json.NewDecoder(req.Body).Decode(&completion)
			completions <- completion
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
		}
		return w.Result(), nil
	})}
	stopped := make(chan error, 1)
	go func() {
		stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{
			CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: 4, HTTPClient: client,
			PollInterval: time.Millisecond, HeartbeatInterval: 5 * time.Millisecond,
			CapacityLimit:   func() int { return int(limit.Load()) },
			ExecutorFactory: func() Executor { factories.Add(1); return &attemptRunner{started: started, release: release} },
		}, NewFake())
	}()
	defer func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("worker stopped: %v", err)
		}
	}()
	waitStarted := func() ExecuteRequest {
		t.Helper()
		select {
		case req := <-started:
			return req
		case <-ctx.Done():
			t.Fatal("attempt did not start")
			return ExecuteRequest{}
		}
	}
	waitCompletion := func() remoteCompletion {
		t.Helper()
		select {
		case c := <-completions:
			return c
		case <-ctx.Done():
			t.Fatal("completion missing")
			return remoteCompletion{}
		}
	}
	children := make(map[string]ExecuteRequest)
	for i := 0; i < 4; i++ {
		req := waitStarted()
		children[req.AttemptID] = req
	}
	// Four occupied slots must prevent a fifth poll, even with two executor kinds.
	waitHeartbeat := func(count int) {
		t.Helper()
		for {
			select {
			case hb := <-heartbeats:
				if hb.Capacity != nil && *hb.Capacity == int(limit.Load()) && len(hb.Active) == count {
					want := int(limit.Load()) - count
					if want < 0 {
						want = 0
					}
					if hb.AvailableSlots != want {
						t.Errorf("slots = %d, want %d", hb.AvailableSlots, want)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal("heartbeat missing")
			}
		}
	}
	waitHeartbeat(4)
	if next.Load() != 4 {
		t.Fatalf("admitted %d attempts at capacity four", next.Load())
	}
	limit.Store(1)
	revoke.Store(true)
	c := waitCompletion()
	if c.AttemptID != "attempt-0" || c.State != "cancelled" || c.Outputs != nil {
		t.Fatalf("cancel completion: %+v", c)
	}
	waitHeartbeat(3)
	for i := 1; i < 4; i++ {
		if children[fmt.Sprintf("attempt-%d", i)].Context.Err() != nil {
			t.Fatal("sibling was cancelled")
		}
	}
	for i := 1; i < 3; i++ {
		close(release[i])
		if c := waitCompletion(); c.State != "succeeded" {
			t.Fatalf("sibling completion: %+v", c)
		}
	}
	waitHeartbeat(1)
	if next.Load() != 4 {
		t.Fatalf("capacity reduction admitted new work: %d", next.Load())
	}
	close(release[3])
	_ = waitCompletion()
	for i := 4; i < 6; i++ {
		req := waitStarted()
		if req.AttemptID != fmt.Sprintf("attempt-%d", i) {
			t.Fatalf("identity: %+v", req)
		}
		waitHeartbeat(1)
		if next.Load() != int32(i+1) {
			t.Fatal("capacity one exceeded")
		}
		close(release[i])
		if c := waitCompletion(); c.State != "succeeded" {
			t.Fatalf("completion: %+v", c)
		}
	}
	if factories.Load() != 6 {
		t.Fatalf("runner instances: %d", factories.Load())
	}
}

func TestRemoteWorkerStopsWhenProcessIsReplaced(t *testing.T) {
	for _, endpoint := range []string{"heartbeat", "assignments"} {
		t.Run(endpoint, func(t *testing.T) {
			var registrations atomic.Int32
			var instance string
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch {
				case strings.HasSuffix(req.URL.Path, "register"):
					registrations.Add(1)
					var registration remoteRegistration
					if err := json.NewDecoder(req.Body).Decode(&registration); err != nil {
						t.Error(err)
					}
					instance = registration.InstanceID
					if instance == "" {
						t.Error("missing process identity")
					}
					_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
				case strings.HasSuffix(req.URL.Path, "heartbeat"):
					var heartbeat remoteHeartbeat
					_ = json.NewDecoder(req.Body).Decode(&heartbeat)
					if heartbeat.InstanceID != instance {
						t.Error("heartbeat omitted process identity")
					}
					if endpoint == "heartbeat" {
						w.WriteHeader(http.StatusForbidden)
					} else {
						_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
					}
				case strings.HasSuffix(req.URL.Path, "assignments"):
					if req.URL.Query().Get("instanceId") != instance {
						t.Error("poll omitted process identity")
					}
					w.WriteHeader(http.StatusForbidden)
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
				}
				return w.Result(), nil
			})}
			err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client}, NewFake())
			if err == nil || ctx.Err() != nil {
				t.Fatalf("worker did not stop on replacement: %v", err)
			}
			if registrations.Load() != 1 {
				t.Fatalf("replaced process registered again %d times", registrations.Load())
			}
		})
	}
}

// A malformed child and its slow completion receipt must not prevent a sibling
// from running and completing. The failed child retains its slot until ack.
func TestRemoteWorkerPreparationFailureAndPendingReceiptAreIsolated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var next atomic.Int32
	pending := make(chan struct{})
	sibling := make(chan remoteCompletion, 1)
	heartbeat := make(chan remoteHeartbeat, 100)
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(req.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "heartbeat"):
			var hb remoteHeartbeat
			_ = json.NewDecoder(req.Body).Decode(&hb)
			select {
			case heartbeat <- hb:
			default:
			}
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "assignments"):
			i := next.Add(1)
			if i > 2 {
				w.WriteHeader(http.StatusNoContent)
				break
			}
			request := json.RawMessage(`{"executor":"test"}`)
			if i == 1 {
				request = json.RawMessage(`"invalid execute request"`)
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: fmt.Sprint(i), RunID: "run", ActivationID: fmt.Sprint(i), AttemptID: fmt.Sprint(i), CoordinatorEpoch: 1, Generation: 1, Request: request})
		case strings.HasSuffix(req.URL.Path, "complete"):
			var completion remoteCompletion
			_ = json.NewDecoder(req.Body).Decode(&completion)
			if completion.AssignmentID == "1" {
				if completion.State != "failed" {
					t.Errorf("preparation failure: %+v", completion)
				}
				select {
				case <-pending:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			} else {
				sibling <- completion
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
		}
		return w.Result(), nil
	})}
	stopped := make(chan error, 1)
	go func() {
		stopped <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: 2, HTTPClient: client, HeartbeatInterval: time.Millisecond, PollInterval: time.Millisecond, ExecutorFactory: func() Executor { return NewFake() }}, nil)
	}()
	defer func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	select {
	case completion := <-sibling:
		if completion.State != "succeeded" {
			t.Fatalf("sibling: %+v", completion)
		}
	case <-ctx.Done():
		t.Fatal("pending completion blocked sibling")
	}
	for {
		select {
		case hb := <-heartbeat:
			if len(hb.Active) == 1 && hb.Active[0] == "1" && hb.AvailableSlots == 1 {
				close(pending)
				return
			}
		case <-ctx.Done():
			t.Fatal("pending receipt lost its admission slot or stopped heartbeats")
		}
	}
}
