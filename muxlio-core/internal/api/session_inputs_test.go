package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestSessionInputAPI(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "run", State: "running", PlanDigest: "pinned"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.CompareAndSwapSessionQueue(&state.SessionQueue{Version: "1", RunID: "run", Handle: contract.SessionHandle{ID: "s", ExpiresAt: time.Now().Add(time.Hour)}, Boundary: true, Permissions: []string{"review"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := WithAuth(NewRouter(s, nil), "admin", "worker",
		AccessGrant{Token: "writer", RunID: "run", Permissions: []string{"steer", "review", "read"}},
		AccessGrant{Token: "denied", RunID: "run", Permissions: []string{"steer"}},
		AccessGrant{Token: "reader", RunID: "run", Permissions: []string{"read"}},
		AccessGrant{Token: "other", RunID: "other", Permissions: []string{"steer", "review"}})
	call := func(method, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/runs/run/sessions/s/inputs", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"version":"1","requestId":"id","message":"new direction","policy":"live-or-next-boundary"}`
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"worker", body, 403}, {"denied", body, 403}, {"other", body, 403}, {"reader", body, 403},
		{"writer", `{"version":"1","requestId":"live","message":"hi","policy":"live-only"}`, 422},
		{"writer", body, 202}, {"writer", body, 202},
		{"writer", `{"version":"1","requestId":"id","message":"changed","policy":"live-or-next-boundary"}`, 409},
	} {
		w := call("POST", tc.token, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	w := call("GET", "reader", "")
	var q state.SessionQueue
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil || w.Code != 200 || len(q.Inputs) != 1 || q.Inputs[0].Status != "accepted" {
		t.Fatalf("reconnect: %s %v", w.Body.String(), err)
	}
	if err := state.AcknowledgeSessionInputs(s, "run", "s", []contract.SessionInput{q.Inputs[0].SessionInput}, []string{"id"}); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "reader", "")
	if !strings.Contains(w.Body.String(), `"status":"delivered"`) {
		t.Fatal(w.Body.String())
	}
	if err := s.SetRunState("run", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "writer", body); w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"delivered"`) {
		t.Fatalf("lost response retry after completion: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "writer", strings.Replace(body, `"id"`, `"new"`, 1)); w.Code != 409 {
		t.Fatalf("terminal run accepted new input: %d %s", w.Code, w.Body.String())
	}
	run, _ := s.GetRun("run")
	if run.PlanDigest != "pinned" {
		t.Fatal("steering changed plan")
	}
}

func TestLiveInputRequiresPersistedOffer(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(s)
	handle := contract.SessionHandle{ID: "s", ExpiresAt: time.Now().Add(time.Hour)}
	q := &state.SessionQueue{Version: "1", RunID: "run", Handle: handle, Boundary: true, Live: true}
	if _, err := s.CompareAndSwapSessionQueue(q, 0); err != nil {
		t.Fatal(err)
	}
	input := state.QueuedSessionInput{SessionInput: contract.SessionInput{Version: "1", RequestID: "id", Message: "direction"}, Policy: "live-or-next-boundary", CallerID: "operator"}
	if _, err := state.AcceptSessionInput(s, "run", "s", input); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{RunID: "run", ActivationID: "active", AttemptID: "active", Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: &handle}}
	raw, _ := json.Marshal(req)
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "active", RunID: "run", ActivationID: "active", AttemptID: "active", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	// Claim directly: this test exercises receipt fencing with a generic fixture.
	if _, err := s.ClaimAssignment(state.Assignment{AssignmentID: "active", RunID: "run", ActivationID: "active", AttemptID: "active", WorkerID: "worker", CoordinatorEpoch: c.epoch, LeaseUntil: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, err := s.LookupWorkerAssignment("worker", "active")
	if err != nil || a == nil {
		t.Fatalf("assignment: %+v %v", a, err)
	}
	receipt := liveInputReceipt{AssignmentID: a.AssignmentID, Generation: a.Generation, CoordinatorEpoch: a.CoordinatorEpoch, AcknowledgedInputs: []string{"id"}}
	if err := c.acknowledgeLiveInputs("worker", []liveInputReceipt{receipt}); err == nil {
		t.Fatal("acknowledged input before offering it")
	}
	if command, err := c.liveInputCommand(a); err != nil || command == nil {
		t.Fatalf("offer: %+v %v", command, err)
	}
	// An unacknowledged live delivery falls back to the next durable boundary.
	if inputs, err := state.BoundaryInputs(s, "run", "s", "next"); err != nil || len(inputs) != 1 {
		t.Fatalf("fallback: %+v %v", inputs, err)
	}
	stale := receipt
	stale.Generation++
	if err := c.acknowledgeLiveInputs("worker", []liveInputReceipt{stale}); err != nil {
		t.Fatal(err)
	}
	q, _ = s.GetSessionQueue("run", "s")
	if q.Inputs[0].Status != "accepted" {
		t.Fatal("stale receipt delivered input")
	}
	if err := c.acknowledgeLiveInputs("worker", []liveInputReceipt{receipt, receipt}); err != nil {
		t.Fatal(err)
	}
	q, _ = s.GetSessionQueue("run", "s")
	if q.Inputs[0].Status != "delivered" {
		t.Fatal("delayed live receipt was lost after boundary fallback")
	}

	input.RequestID, input.Policy = "boundary", "next-boundary"
	if _, err := state.AcceptSessionInput(s, "run", "s", input); err != nil {
		t.Fatal(err)
	}
	inputs, err := state.BoundaryInputs(s, "run", "s", "boundary")
	if err != nil || len(inputs) != 1 {
		t.Fatalf("boundary inputs: %+v %v", inputs, err)
	}
	req.AttemptID, req.ActivationID, req.SessionInputs = "boundary", "boundary", inputs
	raw, _ = json.Marshal(req)
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "boundary", RunID: "run", ActivationID: "boundary", AttemptID: "boundary", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	generation, err := s.ClaimAssignment(state.Assignment{AssignmentID: "boundary", RunID: "run", ActivationID: "boundary", AttemptID: "boundary", WorkerID: "worker", CoordinatorEpoch: c.epoch, LeaseUntil: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// No Execute call is waiting. Completion must still publish a durable input
	// receipt, including when the worker retries after a lost HTTP response.
	completion := WorkerCompletion{AssignmentID: "boundary", RunID: "run", ActivationID: "boundary", AttemptID: "boundary", Generation: generation, CoordinatorEpoch: c.epoch, State: "succeeded", AcknowledgedInputs: []string{"boundary"}}
	invalid := completion
	invalid.AcknowledgedInputs = []string{"unoffered"}
	if err := c.Complete("worker", invalid); err == nil {
		t.Fatal("accepted an unoffered acknowledgment")
	}
	if result, err := s.WorkerResult("boundary"); err != nil || result != nil {
		t.Fatalf("invalid acknowledgment persisted a result: %s %v", result, err)
	}
	if a, err := s.LookupWorkerAssignment("worker", "boundary"); err != nil || a == nil || a.State != "active" {
		t.Fatalf("invalid acknowledgment completed assignment: %+v %v", a, err)
	}
	for range 2 {
		if err := c.Complete("worker", completion); err != nil {
			t.Fatal(err)
		}
	}
	q, _ = s.GetSessionQueue("run", "s")
	if q.Inputs[1].Status != "delivered" {
		t.Fatal("boundary receipt depended on a connected Execute caller")
	}
}

type inputDeliveryExecutor struct {
	handle  *contract.SessionHandle
	started chan workers.ExecuteRequest
	release chan struct{}
}

func (e *inputDeliveryExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	result := workers.ExecuteResult{Session: e.handle}
	if req.Session.Mode == "create" {
		return result, nil
	}
	e.started <- req
	if req.Action == "steer" || req.ActivationID == "boundary" {
		for _, input := range req.SessionInputs {
			result.AcknowledgedInputs = append(result.AcknowledgedInputs, input.RequestID)
		}
		return result, nil
	}
	select {
	case <-e.release:
		return result, nil
	case <-req.Context.Done():
		return workers.ExecuteResult{}, req.Context.Err()
	}
}

func TestSessionInputDeliveryThroughWorker(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "boundary"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			s, err := sqlite.Open(filepath.Join(t.TempDir(), "queue.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
				t.Fatal(err)
			}
			c := NewCoordinator(s)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(WithAuth(NewRouter(s, nil, NewWorkerProtocolHandler(c)), "admin", "worker"))
			defer server.Close()
			manifest := &contract.AdapterManifest{Executor: "test", Capabilities: map[string]bool{contract.CapabilitySession: true, contract.CapabilitySessionInput: true, contract.CapabilityLiveInput: live}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}
			handle := &contract.SessionHandle{ID: "s", Executor: "test", RunnerVersion: "1", OwnerWorker: "worker", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}
			fixture := &inputDeliveryExecutor{handle: handle, started: make(chan workers.ExecuteRequest, 4), release: make(chan struct{})}
			stopped := make(chan error, 1)
			go func() {
				stopped <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: server.URL, Token: "worker", WorkerID: "worker", Capacity: 1, Capabilities: []workers.RemoteCapability{{Name: "test", Contract: manifest}}, Labels: map[string]string{workers.SessionEnvironmentLabel: "env"}, PollInterval: time.Millisecond, HeartbeatInterval: 5 * time.Millisecond, WorkBase: t.TempDir()}, fixture)
			}()
			defer func() {
				cancel()
				if err := <-stopped; err != nil {
					t.Error(err)
				}
			}()
			for {
				registered, err := s.ListWorkers(time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				if len(registered) > 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("registration timeout")
				case <-time.After(time.Millisecond):
				}
			}
			create := workers.ExecuteRequest{Context: ctx, RunID: "run", ActivationID: "create", AttemptID: "create", Executor: "test", Session: &contract.SessionRequest{Mode: "create"}}
			if _, err := c.Execute(create); err != nil {
				t.Fatal(err)
			}
			req := create
			req.ActivationID = "active"
			req.AttemptID = "active"
			req.Session = &contract.SessionRequest{Mode: "resume", Handle: handle}
			completed := make(chan error, 1)
			go func() { _, err := c.Execute(req); completed <- err }()
			select {
			case <-fixture.started:
			case <-ctx.Done():
				t.Fatal("turn did not start")
			}
			input := state.QueuedSessionInput{SessionInput: contract.SessionInput{Version: "1", RequestID: "id", Message: "new direction"}, CallerID: "operator", Policy: "live-or-next-boundary"}
			for i := 0; i < 2; i++ {
				if _, err := state.AcceptSessionInput(s, "run", "s", input); err != nil {
					t.Fatal(err)
				}
			}
			if live {
				select {
				case delivery := <-fixture.started:
					if delivery.Action != "steer" || len(delivery.SessionInputs) != 1 {
						t.Fatalf("live delivery: %+v", delivery)
					}
				case <-ctx.Done():
					t.Fatal("live input not delivered at capacity one")
				}
				for {
					q, err := s.GetSessionQueue("run", "s")
					if err != nil {
						t.Fatal(err)
					}
					if q.Inputs[0].Status == "delivered" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("live receipt not persisted")
					case <-time.After(time.Millisecond):
					}
				}
			} else {
				q, _ := s.GetSessionQueue("run", "s")
				if q.Inputs[0].Status != "accepted" || q.Inputs[0].Delivery != "next-boundary" {
					t.Fatalf("unsupported live input: %+v", q)
				}
			}
			close(fixture.release)
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			req.ActivationID = "boundary"
			req.AttemptID = "boundary"
			if _, err := c.Execute(req); err != nil {
				t.Fatal(err)
			}
			q, err := s.GetSessionQueue("run", "s")
			if err != nil || len(q.Inputs) != 1 || q.Inputs[0].Status != "delivered" {
				t.Fatalf("receipt: %+v %v", q, err)
			}
			// A successful live receipt must remove the input from later boundary batches.
			for len(fixture.started) > 0 {
				delivery := <-fixture.started
				if delivery.ActivationID == "boundary" {
					want := 1
					if live {
						want = 0
					}
					if len(delivery.SessionInputs) != want {
						t.Fatalf("boundary batch: %+v", delivery.SessionInputs)
					}
				}
			}
		})
	}
}
