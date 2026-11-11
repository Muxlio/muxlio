package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func budgetPtr[T any](v T) *T { return &v }

func budgetPlan(limits *RunLimits, concurrent bool) *Plan {
	n := 8
	if !concurrent {
		n = 1
	}
	return &Plan{IRVersion: "1.0", Name: "budget", Root: "root", Limits: limits, Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "fanout", Kind: KindMap, Items: &Expr{Op: "literal", Value: json.RawMessage(`[0,1,2,3,4,5,6,7]`)}, BodyRef: "body", Concurrency: &n}}},
		"body": {Nodes: []Node{{ID: "work", Kind: KindExecute, Executor: "test"}}},
	}}
}

func TestRunHardLimitsBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name               string
		limits             *RunLimits
		calls, activations int
	}{
		{"attempts", &RunLimits{MaxAttempts: budgetPtr(2)}, 2, 9},
		{"activations", &RunLimits{MaxActivations: budgetPtr(3)}, 2, 3},
		{"no attempts", &RunLimits{MaxAttempts: budgetPtr(0)}, 0, 9},
		{"no activations", &RunLimits{MaxActivations: budgetPtr(0)}, 0, 0},
		{"no active attempts", &RunLimits{MaxActiveAttempts: budgetPtr(0)}, 0, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := workers.NewFake()
			e := NewEngine(budgetPlan(tc.limits, true), fake)
			id, err := e.CreateRun(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			err = e.Run(id)
			run, _ := e.store.GetRun(id)
			acts, _ := e.store.ListActivations(id)
			if err == nil || !strings.Contains(run.Error, "run budget exhausted") || run.State != RunFailed {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			if len(fake.Calls()) != tc.calls || len(acts) > tc.activations {
				t.Fatalf("calls=%d activations=%d", len(fake.Calls()), len(acts))
			}
		})
	}
}

func TestRunAttemptsIncludeRetriesAndSurviveRecovery(t *testing.T) {
	p := budgetPlan(&RunLimits{MaxAttempts: budgetPtr(2)}, false)
	sg := p.Subgraphs["body"]
	sg.Nodes[0].Retry = &RetryPolicy{MaxAttempts: 5, Effect: "retryable"}
	p.Subgraphs["body"] = sg
	fake := workers.NewFake().ByNode("work", workers.ExecuteResult{Error: "try again"})
	e := NewEngine(p, fake)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil {
		t.Fatal("expected exhaustion")
	}
	recovered := NewEngineWithStore(nil, fake, e.store)
	if err = recovered.Retry(id); err != nil {
		t.Fatal(err)
	}
	if err = recovered.Run(id); err == nil {
		t.Fatal("recovery reset the budget")
	}
	if len(fake.Calls()) != 2 {
		t.Fatalf("calls=%d", len(fake.Calls()))
	}
}

func TestUsageLimitsAndMissingUsagePolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits *RunLimits
		result workers.ExecuteResult
		calls  int
		reason string
	}{

		{"closed tokens", selectorLimit("tokens", "10", "", ""), workers.ExecuteResult{}, 1, "missing-accounting"},
		{"open tokens", selectorLimit("tokens", "10", "fail-open", ""), workers.ExecuteResult{}, 8, ""},
		{"reported zero", selectorLimit("tokens", "10", "", ""), workers.ExecuteResult{Accounting: budgetAccounting("tokens", "0", false)}, 8, ""},
		{"tokens", selectorLimit("tokens", "10", "", ""), workers.ExecuteResult{Accounting: budgetAccounting("tokens", "11", false)}, 1, "budget-exhausted"},
		{"cost", selectorLimit("cost", "0.5", "", ""), workers.ExecuteResult{Accounting: budgetAccounting("cost", "0.6", false)}, 1, "budget-exhausted"},
		{"closed cost", selectorLimit("cost", "0.5", "", ""), workers.ExecuteResult{}, 1, "missing-accounting"},
		{"estimate closed", selectorLimit("tokens", "10", "", ""), workers.ExecuteResult{Accounting: budgetAccounting("tokens", "11", true)}, 1, "missing-accounting"},
		{"estimate opt in", selectorLimit("tokens", "10", "fail-open", "collected-or-estimated"), workers.ExecuteResult{Accounting: budgetAccounting("tokens", "11", true)}, 1, "budget-exhausted"},
		{"cost estimate opt in", selectorLimit("cost", "0.5", "fail-open", "collected-or-estimated"), workers.ExecuteResult{Accounting: budgetAccounting("cost", "0.6", true)}, 1, "budget-exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := workers.NewFake().ByNode("work", tc.result)
			e := NewEngine(budgetPlan(tc.limits, false), fake)
			id, err := e.CreateRun(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			err = e.Run(id)
			if (err == nil) != (tc.reason == "") || (err != nil && !strings.Contains(err.Error(), tc.reason)) {
				t.Fatalf("err=%v", err)
			}
			if len(fake.Calls()) != tc.calls {
				t.Fatalf("calls=%d", len(fake.Calls()))
			}
			attempts, _ := e.store.ListExecutionAttempts(id, "")
			if tc.result.Accounting == nil && attempts[0].Accounting != nil {
				t.Fatal("missing tokens became zero")
			}
		})
	}
}

func TestRunActiveAttemptAdmissionIsAtomic(t *testing.T) {
	e := NewEngine(&Plan{Limits: &RunLimits{MaxActiveAttempts: budgetPtr(2)}}, workers.NewFake())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = e.admitAttempt(state.ExecutionAttempt{ID: fmt.Sprint(i), RunID: "run", State: "running"})
		}(i)
	}
	wg.Wait()
	attempts, _ := e.store.ListExecutionAttempts("run", "")
	if len(attempts) != 2 {
		t.Fatalf("admitted=%d", len(attempts))
	}
	// Completion frees capacity; admission is based on durable state.
	a := attempts[0]
	a.State = "succeeded"
	a.FinishedAt = time.Now()
	if err := e.store.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err := e.admitAttempt(state.ExecutionAttempt{ID: "next", RunID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationCannotBypassAttemptLimit(t *testing.T) {
	x := &effectExecutor{lookup: "completed"}
	e := NewEngine(&Plan{Limits: &RunLimits{MaxAttempts: budgetPtr(1)}}, x)
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "reconcilable", MaxAttempts: 2}}, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "maxAttempts") || len(x.calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(x.calls), err)
	}
}

func TestRunLimitValidation(t *testing.T) {
	for _, l := range []*RunLimits{{MaxAttempts: budgetPtr(-1)}, {MaxActivations: budgetPtr(-1)}, {MaxActiveAttempts: budgetPtr(-1)}, {Accounting: []accounting.Limit{{Kind: "tokens", Maximum: "-1"}}}, {Accounting: []accounting.Limit{{Kind: "cost", Maximum: "0.5"}}}, {Accounting: []accounting.Limit{{Kind: "tokens", Maximum: "10", OnMissing: "ignore"}}}} {
		if !HasErrors(Validate(budgetPlan(l, false))) {
			t.Fatalf("accepted %+v", l)
		}
	}
}

func TestBudgetFailureCannotBeRetriedPastCompletedAttempt(t *testing.T) {
	p := budgetPlan(selectorLimit("tokens", "1", "", ""), false)
	p.Subgraphs["root"] = Subgraph{Nodes: []Node{{ID: "work", Kind: KindExecute, Executor: "test"}}}
	fake := workers.NewFake().ByNode("work", workers.ExecuteResult{Accounting: budgetAccounting("tokens", "2", false)})
	e := NewEngine(p, fake)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil {
		t.Fatal("expected exhaustion")
	}
	if err = e.Retry(id); err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil {
		t.Fatal("retry bypassed usage limit")
	}
	if len(fake.Calls()) != 1 {
		t.Fatal("retry repeated completed work")
	}
}

type measuredReconciliationExecutor struct{}

func (measuredReconciliationExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	if req.Action == "reconcile" {
		return workers.ExecuteResult{Accounting: testaccounting.Tokens(1, 0, 0), Reconciliation: &contract.Reconciliation{Status: "completed"}}, nil
	}
	return workers.ExecuteResult{Accounting: testaccounting.Tokens(100, 0, 0)}, workers.UncertainExecutionError{}
}

func TestReconciliationUsageBelongsToItsOwnInvocation(t *testing.T) {
	e := NewEngine(&Plan{}, measuredReconciliationExecutor{})
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "reconcilable"}}, nil, &EvalContext{})
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := e.store.ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	summary, summaryErr := state.SummarizeAccounting(attempts)
	if len(attempts) != 2 || summaryErr != nil || summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != "101" || summary.Tokens.Attempts.Unknown != 0 {
		t.Fatalf("attempts=%+v summary=%+v", attempts, summary)
	}
}

func TestDirectRecoveryReplayConsumesNewAttemptSlot(t *testing.T) {
	for _, effect := range []string{"pure", "idempotent-with-key"} {
		t.Run(effect, func(t *testing.T) {
			fake := workers.NewFake()
			e := NewEngine(&Plan{Limits: &RunLimits{MaxAttempts: budgetPtr(1)}}, fake)
			if err := e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "run:node-attempt-1", RunID: "run", ActivationID: "run:node", RetryNumber: 1, State: "running", Effect: effect, IdempotencyKey: "key"}); err != nil {
				t.Fatal(err)
			}
			_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: effect}}, nil, &EvalContext{})
			if err == nil || !strings.Contains(err.Error(), "maxAttempts") || len(fake.Calls()) != 0 {
				t.Fatalf("calls=%d err=%v", len(fake.Calls()), err)
			}
		})
	}
}

func TestActivationLimitIncludesNestedLoopsAndFanout(t *testing.T) {
	p := budgetPlan(&RunLimits{MaxActivations: budgetPtr(8)}, false)
	p.Subgraphs["root"] = Subgraph{Nodes: []Node{{ID: "child", Kind: KindChild, ChildRef: "child", MaxDepth: budgetPtr(1)}}}
	p.Subgraphs["child"] = Subgraph{Nodes: []Node{{ID: "loop", Kind: KindLoop, BodyRef: "iteration", MaxIterations: budgetPtr(10), While: &Expr{Op: "literal", Value: json.RawMessage(`true`)}}}}
	p.Subgraphs["iteration"] = Subgraph{Nodes: []Node{{ID: "fanout", Kind: KindMap, BodyRef: "body", Items: &Expr{Op: "literal", Value: json.RawMessage(`[0,1]`)}}}}
	fake := workers.NewFake()
	e := NewEngine(p, fake)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil || !strings.Contains(err.Error(), "maxActivations") {
		t.Fatalf("err=%v", err)
	}
	acts, err := e.store.ListActivations(id)
	if err != nil || len(acts) != 8 || len(fake.Calls()) != 4 {
		t.Fatalf("activations=%d calls=%d err=%v", len(acts), len(fake.Calls()), err)
	}
	run, _ := e.store.GetRun(id)
	if run.State != RunFailed || !strings.Contains(run.Error, "maxActivations") {
		t.Fatalf("run=%+v", run)
	}
}

func TestAttemptLimitSurvivesSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := workers.NewFake()
	e := NewEngineWithStore(budgetPlan(&RunLimits{MaxAttempts: budgetPtr(2)}, false), fake, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil || !strings.Contains(err.Error(), "maxAttempts") {
		t.Fatalf("err=%v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e = NewEngineWithStore(nil, fake, s)
	if err = e.Retry(id); err != nil {
		t.Fatal(err)
	}
	if err = e.Run(id); err == nil || !strings.Contains(err.Error(), "maxAttempts") {
		t.Fatalf("err=%v", err)
	}
	if len(fake.Calls()) != 2 {
		t.Fatalf("reopen reset invocation consumption: calls=%d", len(fake.Calls()))
	}
}

func TestExpiredRunBudgetRecordsTerminalReason(t *testing.T) {
	fake := workers.NewFake()
	e := NewEngine(budgetPlan(nil, false), fake)
	e.plan.DeadlineSeconds = budgetPtr(0.001)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err = e.Run(id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	run, _ := e.store.GetRun(id)
	events, err := e.store.GetEvents(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "run.interrupted" && event.Payload["reason"] == "run deadline exceeded" {
			found = true
		}
	}
	if run.State != RunCancelled || len(fake.Calls()) != 0 || !found {
		t.Fatalf("run=%+v calls=%d reason recorded=%v", run, len(fake.Calls()), found)
	}
}

type interruptedLookupExecutor struct{ err error }

func (x interruptedLookupExecutor) Execute(workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return workers.ExecuteResult{}, x.err
}

func TestInterruptedReconciliationRetainsActiveAttemptSlot(t *testing.T) {
	for _, interrupted := range []error{context.Canceled, context.DeadlineExceeded, workers.UncertainExecutionError{}} {
		t.Run(interrupted.Error(), func(t *testing.T) {
			e := NewEngine(&Plan{Limits: &RunLimits{MaxActiveAttempts: budgetPtr(2)}}, interruptedLookupExecutor{interrupted})
			a := state.ExecutionAttempt{ID: "original", RunID: "run", ActivationID: "run:node", State: "running", Effect: "reconcilable"}
			_, _, err := e.reconcileEffect(workers.ExecuteRequest{Executor: "test"}, a, &Node{ID: "node"})
			if err == nil {
				t.Fatal("accepted interrupted lookup")
			}
			if err = e.admitAttempt(state.ExecutionAttempt{ID: "next", RunID: "run", State: "running"}); err == nil || !strings.Contains(err.Error(), "maxActiveAttempts") {
				t.Fatalf("interrupted lookup freed its active slot: %v", err)
			}
			queries, err := e.store.ListExecutionAttempts("run", "original-reconciliation")
			if err != nil || len(queries) != 1 || queries[0].AwaitReconciliationReason == "" {
				t.Fatalf("query requires an explicit resolution reason: %+v err=%v", queries, err)
			}
			if err = e.store.ResolveExecutionAttempt("run", queries[0].ID, state.AttemptResolution{Outcome: "not-applied", Reason: "query termination confirmed"}); err != nil {
				t.Fatal(err)
			}
			if err = e.admitAttempt(state.ExecutionAttempt{ID: "next", RunID: "run", State: "running"}); err != nil {
				t.Fatalf("resolution did not release capacity: %v", err)
			}
		})
	}
}

func selectorLimit(kind, maximum, onMissing, evidence string) *RunLimits {
	l := accounting.Limit{Kind: kind, Maximum: maximum, OnMissing: onMissing, Evidence: evidence}
	if kind == "cost" {
		l.Currency = "USD"
		l.Basis = "api-equivalent"
	}
	return &RunLimits{Accounting: []accounting.Limit{l}}
}
func budgetAccounting(kind, value string, estimated bool) *contract.Accounting {
	key := contract.AccountingKey{Kind: kind}
	entry := contract.AccountingEntry{ID: "call"}
	if kind == "tokens" {
		m := contract.Measure[contract.Count]{}
		r := &contract.Reading[contract.Count]{Value: contract.Count(value), Source: "fixture", Fidelity: "metered"}
		if estimated {
			r.Method = "fixture"
			r.Version = "1"
			m.Estimated = r
		} else {
			m.Collected = r
		}
		entry.Tokens = &contract.TokenMeasures{Total: m}
	} else {
		key.Currency = "USD"
		key.Basis = "api-equivalent"
		m := contract.Measure[contract.Amount]{}
		r := &contract.Reading[contract.Amount]{Value: contract.Amount(value), Source: "fixture", Fidelity: "metered"}
		if estimated {
			r.Method = "fixture"
			r.Version = "1"
			m.Estimated = r
		} else {
			m.Collected = r
		}
		entry.Costs = []contract.CostMeasure{{Currency: "USD", Basis: "api-equivalent", Amount: m}}
	}
	return &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{entry}, Coverage: []contract.MeterCoverage{{Key: key, State: "complete"}}}
}
