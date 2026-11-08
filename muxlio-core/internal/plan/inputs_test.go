package plan

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestInputWaitUsesNoWorkerAndRetainsIdentity(t *testing.T) {
	s := state.New()
	_ = s.CreateRun(&state.Run{ID: "r", State: RunRunning})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngineWithStore(&Plan{}, nil, s)
	e.Context = ctx
	node := &Node{ID: "ask", Kind: KindWait, WaitFor: "input", SignalName: "review"}
	done := make(chan error, 2)
	for _, scope := range []string{"one", "two"} {
		_ = s.CreateActivation(&state.Activation{ID: activationID("r", []string{scope}, "ask"), RunID: "r", State: ActRunning})
		go func(scope string) {
			out, err := e.executeWait("r", node, []string{scope}, &EvalContext{})
			if err == nil && out["response"] != scope {
				t.Errorf("response for %s: %v", scope, out)
			}
			done <- err
		}(scope)
	}
	var requests []state.InputRequest
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		requests, _ = s.ListInputRequests("r")
		if len(requests) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(requests) != 2 || requests[0].RequestID == requests[1].RequestID {
		t.Fatalf("identities: %+v", requests)
	}
	for _, req := range requests {
		act, _ := s.GetActivation(req.ActivationID)
		if act.State != ActWaiting {
			t.Fatalf("activation should wait: %+v", act)
		}
		attempts, _ := s.ListExecutionAttempts("r", req.ActivationID)
		if len(attempts) != 0 {
			t.Fatalf("wait dispatched a worker: %+v", attempts)
		}
		assignments, _ := s.ListAssignments("r")
		if len(assignments) != 0 {
			t.Fatalf("wait holds a worker slot: %+v", assignments)
		}
		response := "one"
		if req.ActivationID == activationID("r", []string{"two"}, "ask") {
			response = "two"
		}
		if _, _, err := s.SubmitInputResponse("r", req.RequestID, response, "caller", req.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("wait did not resume")
		}
	}
}

func TestInputWaitRecoveryAndExpiration(t *testing.T) {
	s := state.New()
	_ = s.CreateRun(&state.Run{ID: "r", State: RunRunning})
	node := &Node{ID: "ask", Kind: KindWait, WaitFor: "input", SignalName: "question"}
	_ = s.CreateActivation(&state.Activation{ID: "r:ask", RunID: "r", State: ActWaiting})
	id := inputRequestID("r:ask", "question")
	_ = s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: id, ActivationID: "r:ask", Name: "question"})
	_, _, err := s.SubmitInputResponse("r", id, false, "agent", "key")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngineWithStore(&Plan{}, nil, s)
	for i := 0; i < 2; i++ {
		out, err := e.executeWait("r", node, nil, &EvalContext{})
		if err != nil || out["response"] != false {
			t.Fatalf("recovery: %v %v", out, err)
		}
	}
	requests, _ := s.ListInputRequests("r")
	if len(requests) != 1 {
		t.Fatal("recovery created another request")
	}
	seconds := 0.01
	node = &Node{ID: "expire", Kind: KindWait, WaitFor: "input", SignalName: "question", RequestExpiresSeconds: &seconds}
	_ = s.CreateActivation(&state.Activation{ID: "r:expire", RunID: "r", State: ActRunning})
	if _, err := e.executeWait("r", node, nil, &EvalContext{}); err == nil {
		t.Fatal("expired wait succeeded")
	}
}

func TestInputRequestSchemaValidation(t *testing.T) {
	for _, schema := range []string{`{"type":"bogus"}`, `{"$ref":"https://example.com/schema"}`} {
		failure, ok := ValidateInputResponse(json.RawMessage(schema), nil).(*ValueValidationFailure)
		if !ok || failure.Code != InvalidSchema {
			t.Fatalf("bad schema accepted: %s %v", schema, failure)
		}
	}
	for _, value := range []any{true, "human", nil, []any{1.0}} {
		raw, _ := json.Marshal(value)
		if err := ValidateInputResponse(json.RawMessage(`{}`), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInputWaitSurvivesCoordinatorRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inputs.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.CreateRun(&state.Run{ID: "r", State: RunRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:ask", RunID: "r", State: ActRunning}); err != nil {
		t.Fatal(err)
	}
	node := &Node{ID: "ask", Kind: KindWait, WaitFor: "input", SignalName: "review"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := NewEngineWithStore(&Plan{}, nil, s)
	engine.Context = ctx
	done := make(chan error, 1)
	go func() {
		_, err := engine.executeWait("r", node, nil, &EvalContext{})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		act, err := s.GetActivation("r:ask")
		if err != nil {
			t.Fatal(err)
		}
		if act.State == ActWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input request did not enter waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := inputRequestID("r:ask", "review")
	// Delivery only needs durable storage; no engine or worker is running.
	if _, accepted, err := s.SubmitInputResponse("r", id, false, "human", "delivery"); err != nil || !accepted {
		t.Fatalf("offline delivery: accepted=%v err=%v", accepted, err)
	}
	engine = NewEngineWithStore(&Plan{}, nil, s)
	out, err := engine.executeWait("r", node, nil, &EvalContext{})
	if err != nil || out["response"] != false {
		t.Fatalf("recovered response: %v %v", out, err)
	}
	requests, err := s.ListInputRequests("r")
	if err != nil || len(requests) != 1 || requests[0].RequestID != id {
		t.Fatalf("recovered identities: %v %v", requests, err)
	}
}

func TestInputWaitReusesFulfilledResponseAfterRetry(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "retry.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.CreateRun(&state.Run{ID: "r"}))
			must(s.CreateActivation(&state.Activation{ID: "r:ask", RunID: "r", NodeID: "ask", State: ActWaiting}))
			original := inputRequestID("r:ask", "approval")
			must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: original, ActivationID: "r:ask", Name: "approval"}))
			must(s.SetRunState("r", RunCancelled))
			must(s.CancelRunWork("r"))
			must(s.RetryRun("r"))
			requests, err := s.ListInputRequests("r")
			must(err)
			var renewed string
			for _, req := range requests {
				if req.State == state.InputRequestPending {
					renewed = req.RequestID
				}
			}
			if renewed == "" {
				t.Fatal("no renewed input")
			}
			_, _, err = s.SubmitInputResponse("r", renewed, false, "human", "delivery")
			must(err)
			node := &Node{ID: "ask", Kind: KindWait, WaitFor: "input", SignalName: "approval"}
			for i := 0; i < 2; i++ {
				engine := NewEngineWithStore(&Plan{}, nil, s)
				out, err := engine.executeWait("r", node, nil, &EvalContext{})
				if err != nil || out["response"] != false {
					t.Fatalf("recovery: %v %v", out, err)
				}
			}
			requests, err = s.ListInputRequests("r")
			must(err)
			if len(requests) != 2 {
				t.Fatalf("recovery created more requests: %+v", requests)
			}
			_, accepted, err := s.SubmitInputResponse("r", renewed, true, "human", "delivery")
			must(err)
			if accepted {
				t.Fatal("recovery allowed duplicate acceptance")
			}
		})
	}
}
