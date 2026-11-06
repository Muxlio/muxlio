package plan

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryRetainsRecordedIdempotencyKey(t *testing.T) {
	s := state.New()
	id := activationID("run", nil, "node")
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1,
		State: "running", Effect: "idempotent-with-key", IdempotencyKey: "persisted-operation-key",
	}); err != nil {
		t.Fatal(err)
	}
	f := &effectExecutor{effects: 1}
	e := NewEngineWithStore(&Plan{}, f, s)
	node := &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "idempotent-with-key"}}
	if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].IdempotencyKey != "persisted-operation-key" {
		t.Fatalf("recovery replaced the stored key: %+v", f.calls)
	}
}

func TestReconciledCompletionMustValidateOutputs(t *testing.T) {
	f := &effectExecutor{lookup: "completed"}
	e := NewEngine(&Plan{}, f)
	node := &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "reconcilable", MaxAttempts: 3},
		Outputs: map[string]OutputDecl{"receipt": {Schema: json.RawMessage(`{"type":"string"}`)}},
	}
	if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err == nil {
		t.Fatal("accepted reconciled completion with a missing receipt")
	}
	if f.effects != 1 {
		t.Fatal("invalid reconciliation repeated the operation")
	}
	attempts, err := e.store.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || !strings.Contains(attempts[0].AwaitReconciliationReason, "reconciled result is invalid") {
		t.Fatalf("missing explanation for invalid reconciliation: %+v %v", attempts, err)
	}
}

type effectExecutor struct {
	calls   []workers.ExecuteRequest
	lookup  string
	reason  string
	effects int
}

func (f *effectExecutor) Execute(r workers.ExecuteRequest) (workers.ExecuteResult, error) {
	f.calls = append(f.calls, r)
	if r.Action == "reconcile" {
		reason := f.reason
		if reason == "" {
			reason = "external ledger checked"
		}
		return workers.ExecuteResult{Reconciliation: &contract.Reconciliation{Status: f.lookup, Reason: reason}}, nil
	}
	f.effects++
	if f.effects == 1 {
		return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "operation-1"}}, workers.UncertainExecutionError{}
	}
	return workers.ExecuteResult{}, nil
}

func TestCrashAfterEffectRequiresResolutionAcrossRestart(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if backend == "sqlite" {
				var err error
				store, err = sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := store.CreateRun(&state.Run{ID: "run", State: "failed"}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetRunState("run", "failed"); err != nil {
				t.Fatal(err)
			}
			id := activationID("run", nil, "node")
			if err := store.CreateActivation(&state.Activation{ID: id, RunID: "run", NodeID: "node", State: "failed"}); err != nil {
				t.Fatal(err)
			}
			// The external system committed, then the worker vanished before completion.
			if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1, State: "running", Executor: "test", Effect: "nonretryable", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if backend == "sqlite" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				store, err = sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer store.Close()
			f := &effectExecutor{}
			e := NewEngineWithStore(&Plan{}, f, store)
			node := &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{MaxAttempts: 3, Effect: "nonretryable"}}
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err == nil {
				t.Fatal("uncertain operation accepted")
			}
			if len(f.calls) != 0 {
				t.Fatal("nonretryable operation blindly repeated")
			}
			attempts, err := store.ListExecutionAttempts("run", id)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].State != "awaiting_reconciliation" || !strings.Contains(attempts[0].AwaitReconciliationReason, "external completion is unknown") {
				t.Fatalf("operator cannot see hold reason: %+v", attempts)
			}
			if err := store.RetryRun("run"); err == nil {
				t.Fatal("run retry bypassed hold")
			}
			if err := store.ResolveExecutionAttempt("run", attempts[0].ID, state.AttemptResolution{Outcome: "not-applied", Reason: "operator verified old operation cannot commit"}); err != nil {
				t.Fatal(err)
			}
			if err := store.RetryRun("run"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdapterReconciliationBeforeRedispatch(t *testing.T) {
	for _, outcome := range []string{"completed", "not-applied", "inconclusive", ""} {
		t.Run(outcome, func(t *testing.T) {
			f := &effectExecutor{lookup: outcome}
			e := NewEngine(&Plan{}, f)
			_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", ConcurrencyKeys: []string{"ledger"}, Retry: &RetryPolicy{Effect: "reconcilable", MaxAttempts: 2}}, nil, &EvalContext{})
			wantEffects := 1
			if outcome == "not-applied" {
				wantEffects = 2
			}
			if f.effects != wantEffects {
				t.Fatalf("effects=%d, want %d", f.effects, wantEffects)
			}
			if len(f.calls) < 2 || f.calls[1].Action != "reconcile" || f.calls[1].RemoteTask == nil || f.calls[1].RemoteTask.ID != "operation-1" {
				t.Fatalf("missing reference lookup: %+v", f.calls)
			}
			if len(f.calls[1].ConcurrencyKeys) != 0 || f.calls[1].Workspace != "" {
				t.Fatal("read-only reconciliation requires the lost operation's resources")
			}
			if (err == nil) != (outcome == "completed" || outcome == "not-applied") {
				t.Fatalf("outcome %q: %v", outcome, err)
			}
			attempts, _ := e.store.ListExecutionAttempts("run", "")
			if err != nil && (len(attempts) != 1 || !strings.Contains(attempts[0].AwaitReconciliationReason, "external ledger checked")) {
				t.Fatalf("missing reconciliation explanation: %+v", attempts)
			}
		})
	}
}

func TestIdempotencyKeySurvivesAttemptAndRunRetry(t *testing.T) {
	for _, key := range []string{"", "invoice-123"} {
		t.Run(fmt.Sprint(key), func(t *testing.T) {
			f := &effectExecutor{}
			e := NewEngine(&Plan{}, f)
			node := &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "idempotent-with-key", MaxAttempts: 2, IdempotencyKey: key}}
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
				t.Fatal(err)
			}
			want := key
			if want == "" {
				want = activationID("run", nil, "node")
			}
			if len(f.calls) != 2 || f.calls[0].IdempotencyKey != want || f.calls[1].IdempotencyKey != want {
				t.Fatalf("key changed: %+v", f.calls)
			}
			// A restart between attempt completion and activation checkpoint reuses result.
			e = NewEngineWithStore(&Plan{}, f, e.store)
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 2 {
				t.Fatal("completed operation repeated after restart")
			}
		})
	}
}

func TestRecoveryAfterNotAppliedResolutionUsesNewAttempt(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if backend == "sqlite" {
				var err error
				store, err = sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			f := &effectExecutor{lookup: "not-applied"}
			node := &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "reconcilable"}}
			e := NewEngineWithStore(&Plan{}, f, store)
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err == nil {
				t.Fatal("single-attempt budget should be exhausted")
			}
			attempts, err := store.ListExecutionAttempts("run", "")
			if err != nil || len(attempts) != 1 || attempts[0].State != "resolved" {
				t.Fatalf("not-applied checkpoint overwritten: %+v %v", attempts, err)
			}
			if backend == "sqlite" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer store.Close()
			// Resume before the activation checkpoint or explicit run retry.
			e = NewEngineWithStore(&Plan{}, f, store)
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 3 || f.calls[2].AttemptID != activationID("run", nil, "node")+"-attempt-2" {
				t.Fatalf("reused the resolved operation identity: %+v", f.calls)
			}
		})
	}
}

func TestReconciliationDropsWorkspaceAffinityAndRedactsAuditReason(t *testing.T) {
	for _, outcome := range []string{"completed", "not-applied"} {
		t.Run(outcome, func(t *testing.T) {
			f := &effectExecutor{lookup: outcome, reason: "ledger checked with sensitive-value-123"}
			s := state.New()
			e := NewEngineWithStore(&Plan{}, f, s)
			labels := map[string]string{workers.WorkspacePlacementLabel: "/lost/workspace", "region": "test"}
			a := state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", Effect: "reconcilable", State: "running"}
			_, _, err := e.reconcileEffect(workers.ExecuteRequest{PlacementLabels: labels, Secrets: map[string]string{"TOKEN": "sensitive-value-123"}}, a, &Node{ID: "node"})
			if err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 1 || f.calls[0].PlacementLabels[workers.WorkspacePlacementLabel] != "" || f.calls[0].PlacementLabels["region"] != "test" {
				t.Fatalf("lookup still needs lost workspace: %+v", f.calls)
			}
			if labels[workers.WorkspacePlacementLabel] != "/lost/workspace" {
				t.Fatal("mutated execution placement")
			}
			events, err := s.GetEvents("run", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "sensitive-value-123") {
				t.Fatalf("secret leaked into resolution audit: %s", raw)
			}
		})
	}
}
