// Package coordinator_test contains integration tests for durable-coordinator features:
// timers, signals, chunked logs, retry policy, wait-node validation, and the HTTP API.
package coordinator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// --- helpers ---

func openDB(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// --- timer tests ---

func TestScheduleAndFireTimer(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_x", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}

	if err := st.ScheduleTimer("t1", "run_x", "act_1", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}

	fired, err := st.DueTimers(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 || fired[0].ID != "t1" {
		t.Fatalf("expected 1 fired timer, got %v", fired)
	}

	// Timer should now be in 'fired' state and not returned again.
	again, err := st.DueTimers(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no more due timers, got %d", len(again))
	}
}

func TestCancelTimer(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_x", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}

	due := time.Now().Add(-time.Second)
	if err := st.ScheduleTimer("t2", "run_x", "act_2", due, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelTimer("t2"); err != nil {
		t.Fatal(err)
	}

	fired, err := st.DueTimers(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 0 {
		t.Fatalf("cancelled timer should not fire, got %v", fired)
	}
}

func TestTimerSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coordinator.db")

	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&state.Run{ID: "run_t", WorkflowName: "w"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	due := time.Now().Add(50 * time.Millisecond)
	if err := st.ScheduleTimer("tmr1", "run_t", "act_t", due, map[string]any{"x": 1}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	time.Sleep(80 * time.Millisecond)
	fired, err := st2.DueTimers(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired timer after reopen, got %d", len(fired))
	}
	if fired[0].Payload["x"] != float64(1) {
		t.Fatalf("unexpected payload %v", fired[0].Payload)
	}
}

// --- signal tests ---

func TestSendAndConsumeSignal(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_s", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}

	if err := st.SendSignal("run_s", "reply", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SendSignal("run_s", "reply", map[string]any{"ok": false}); err != nil {
		t.Fatal(err)
	}

	sigs, err := st.ConsumeSignals("run_s", "reply")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(sigs))
	}
	if sigs[0].Seq != 1 || sigs[1].Seq != 2 {
		t.Fatalf("unexpected seqs: %v %v", sigs[0].Seq, sigs[1].Seq)
	}
}

func TestSignalSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coordinator.db")

	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&state.Run{ID: "run_sr", WorkflowName: "w"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.SendSignal("run_sr", "done", map[string]any{"v": 42}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	sigs, err := st2.ConsumeSignals("run_sr", "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 || sigs[0].Payload["v"] != float64(42) {
		t.Fatalf("unexpected signals after reopen: %v", sigs)
	}
}

// --- chunked log tests ---

func TestAppendAndGetLogChunks(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_l", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{
		ID: "act_l", RunID: "run_l", NodeID: "step", State: "running",
	}); err != nil {
		t.Fatal(err)
	}

	chunks := []state.LogChunk{
		{RunID: "run_l", ActivationID: "act_l", Stream: "stdout", Seq: 1, Data: []byte("hello"), Timestamp: time.Now()},
		{RunID: "run_l", ActivationID: "act_l", Stream: "stdout", Seq: 2, Data: []byte(" world"), Timestamp: time.Now()},
	}
	for _, c := range chunks {
		if err := st.AppendLogChunk(c); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.GetLogChunks("run_l", "act_l")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(got))
	}
	if string(got[0].Data) != "hello" || string(got[1].Data) != " world" {
		t.Fatalf("unexpected chunk data: %q %q", got[0].Data, got[1].Data)
	}
}

func TestLogChunkSizeLimit(t *testing.T) {
	st := openDB(t)
	big := make([]byte, 65*1024)
	err := st.AppendLogChunk(state.LogChunk{
		RunID: "r", ActivationID: "a", Stream: "stdout", Seq: 1, Data: big,
	})
	if err == nil {
		t.Fatal("expected error for oversized chunk")
	}
}

// --- assignment / fencing tests ---

func TestClaimAndCompleteAssignment(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_a", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}

	a := state.Assignment{
		RunID: "run_a", ActivationID: "act_a", WorkerID: "w1",
		LeaseUntil: time.Now().Add(time.Minute),
	}
	gen, err := st.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 {
		t.Fatalf("expected generation 1, got %d", gen)
	}

	// Stale generation should not complete.
	ok, err := st.CompleteAssignment("act_a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale generation should not complete")
	}

	ok, err = st.CompleteAssignment("act_a", gen)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("valid generation should complete")
	}
}

func TestReclaim_incrementsGeneration(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_b", WorkflowName: "w"}); err != nil {
		t.Fatal(err)
	}

	a := state.Assignment{RunID: "run_b", ActivationID: "act_b", WorkerID: "w1", LeaseUntil: time.Now().Add(time.Minute)}
	gen1, err := st.ClaimAssignment(a)
	if err != nil || gen1 != 1 {
		t.Fatalf("first claim gen=%d err=%v", gen1, err)
	}
	gen2, err := st.ClaimAssignment(a)
	if err != nil || gen2 != 2 {
		t.Fatalf("second claim gen=%d err=%v", gen2, err)
	}
}

// --- retry policy tests ---

func TestRetryPolicyRetriesOnError(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "retry-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step",
						Kind:     "execute",
						Executor: "test/echo",
						Retry:    &plan.RetryPolicy{MaxAttempts: 3},
					},
				},
			},
		},
	}
	// Override: fail first, succeed later.
	exec2 := &callCountExecutor{failFirst: 2}
	engine := plan.NewEngine(p, exec2)
	input, _ := json.Marshal(map[string]any{})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatal(err)
	}
	if exec2.calls < 3 {
		t.Fatalf("expected at least 3 calls (2 failures + 1 success), got %d", exec2.calls)
	}
}

type callCountExecutor struct {
	calls     int
	failFirst int
}

func (c *callCountExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	c.calls++
	if c.calls <= c.failFirst {
		return workers.ExecuteResult{Error: "transient failure"}, nil
	}
	return workers.ExecuteResult{Outputs: map[string]any{"result": "ok"}}, nil
}

// --- wait-node validation tests ---

func TestValidateWaitNode_timer(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "wait-timer",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "w", Kind: "wait", WaitFor: "timer"}, // missing timerSeconds
				},
			},
		},
	}
	errs := plan.Validate(p)
	hasCode := func(code string) bool {
		for _, e := range errs {
			if e.Code == code {
				return true
			}
		}
		return false
	}
	if !hasCode("MUXLIO_MISSING_TIMER_SECONDS") {
		t.Fatalf("expected MUXLIO_MISSING_TIMER_SECONDS, got %v", errs)
	}
}

func TestValidateWaitNode_signal(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "wait-signal",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "w", Kind: "wait", WaitFor: "signal"}, // missing signalName
				},
			},
		},
	}
	errs := plan.Validate(p)
	for _, e := range errs {
		if e.Code == "MUXLIO_MISSING_SIGNAL_NAME" {
			return
		}
	}
	t.Fatalf("expected MUXLIO_MISSING_SIGNAL_NAME, got %v", errs)
}

func TestValidateWaitNode_unknownType(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "wait-unknown",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "w", Kind: "wait", WaitFor: "webhook"},
				},
			},
		},
	}
	errs := plan.Validate(p)
	for _, e := range errs {
		if e.Code == "MUXLIO_UNKNOWN_WAIT_TYPE" {
			return
		}
	}
	t.Fatalf("expected MUXLIO_UNKNOWN_WAIT_TYPE, got %v", errs)
}

// --- signal wait node (interpreter) ---

func TestWaitNode_SignalDelivered(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "wait-sig",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "w", Kind: "wait", WaitFor: "signal", SignalName: "approval"},
				},
				Output: map[string]plan.Binding{
					"payload": {Ref: &plan.ValueRef{Node: "w", Port: "payload"}},
				},
			},
		},
	}

	st := openDB(t)
	engine := plan.NewEngineWithStore(p, workers.NewFake(), st)
	input, _ := json.Marshal(map[string]any{})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatal(err)
	}

	// Deliver the signal before running (synchronous interpreter reads it immediately).
	if err := st.SendSignal(runID, "approval", map[string]any{"approved": true}); err != nil {
		t.Fatal(err)
	}

	if err := engine.Run(runID); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("run state = %s", run.State)
	}
}

// --- HTTP API tests ---

func TestAPI_ListRuns(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_api1", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewRouter(st, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	runs := body["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
}

func TestAPI_GetRun_NotFound(t *testing.T) {
	st := openDB(t)
	srv := httptest.NewServer(api.NewRouter(st, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/runs/noexist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPI_GetEvents(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_ev", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewRouter(st, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/runs/run_ev/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	events := body["events"].([]any)
	if len(events) == 0 {
		t.Fatal("expected at least one event (run.created)")
	}
}

func TestAPI_SendSignal(t *testing.T) {
	st := openDB(t)
	if err := st.CreateRun(&state.Run{ID: "run_sig", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewRouter(st, nil))
	defer srv.Close()

	body := strings.NewReader(`{"approved":true}`)
	resp, err := http.Post(srv.URL+"/v1/runs/run_sig/signals/approval", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	sigs, err := st.ConsumeSignals("run_sig", "approval")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal in store, got %d", len(sigs))
	}
}

func TestAPI_Healthz(t *testing.T) {
	st := openDB(t)
	srv := httptest.NewServer(api.NewRouter(st, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}
