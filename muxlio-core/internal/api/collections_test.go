package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func collectionPlan(max int, idle float64) *plan.Plan {
	return &plan.Plan{IRVersion: "1.0", Name: "collection", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{
			ID: "map", Kind: plan.KindMap, MaxItems: &max, BodyRef: "body",
			Items: &plan.Expr{Op: "field", Name: "items", Expr: &plan.Expr{Op: "input"}},
			Key:   &plan.Expr{Op: "field", Name: "key", Expr: &plan.Expr{Op: "item"}},
			Appendable: &plan.AppendableCollection{Name: "items", IdleTimeoutSeconds: idle, ItemSchema: json.RawMessage(
				`{"type":"object","properties":{"key":{"type":"string"},"n":{"type":"integer"}},"required":["key","n"],"additionalProperties":false}`)},
		}}},
		"body": {Nodes: []plan.Node{{
			ID: "echo", Kind: plan.KindExecute, Executor: "test/echo",
			Inputs:  map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "item"}}},
			Outputs: map[string]plan.OutputDecl{"value": {}},
		}}},
	}}
}

type collectionExecutor struct {
	requests chan workers.ExecuteRequest
	release  chan struct{}
}

func (e collectionExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.requests <- req
	select {
	case <-e.release:
		return workers.ExecuteResult{Outputs: req.Inputs}, nil
	case <-req.Context.Done():
		return workers.ExecuteResult{}, req.Context.Err()
	}
}

type collectionClient struct {
	handler http.Handler
	runID   string
}

func newCollectionClient(store state.Store, id string) collectionClient {
	return collectionClient{WithAuth(NewRouter(store, nil), "admin", "worker",
		AccessGrant{Token: "inputs", RunID: id, Permissions: []string{"inputs"}},
		AccessGrant{Token: "read", RunID: id, Permissions: []string{"read"}},
		AccessGrant{Token: "other", RunID: "other", Permissions: []string{"inputs"}},
	), id}
}

func (c collectionClient) call(method, suffix, token, body string) *httptest.ResponseRecorder {
	return c.request(method, "/items"+suffix, token, body)
}

func (c collectionClient) request(method, suffix, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/runs/"+c.runID+"/collections"+suffix, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, req)
	return w
}

func TestRunCollectionDeclarations(t *testing.T) {
	store := state.New()
	p := collectionPlan(2, 30)
	root := p.Subgraphs["root"]
	second := root.Nodes[0]
	second.ID = "second"
	second.Appendable = &plan.AppendableCollection{
		Name: "second", IdleTimeoutSeconds: 15,
		ItemSchema: json.RawMessage(`{"type":"object","required":["key"]}`),
	}
	root.Nodes = append(root.Nodes, second)
	p.Subgraphs["root"] = root
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	id, err := engine.CreateRun(json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCollectionClient(store, id)
	for _, tc := range []struct {
		token     string
		status    int
		canAppend bool
	}{
		{"admin", 200, true},
		{"inputs", 200, true},
		{"read", 200, false},
		{"other", 403, false},
		{"", 401, false},
	} {
		w := c.request(http.MethodGet, "", tc.token, "")
		if w.Code != tc.status {
			t.Fatalf("%q: expected %d, got %d: %s", tc.token, tc.status, w.Code, w.Body)
		}
		if w.Code != 200 {
			continue
		}
		var got struct {
			Collections []plan.AppendableCollection
			CanAppend   bool
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := []plan.AppendableCollection{*root.Nodes[0].Appendable, *second.Appendable}
		if !reflect.DeepEqual(got.Collections, want) || got.CanAppend != tc.canAppend {
			t.Fatalf("%q: unexpected declarations: %s", tc.token, w.Body)
		}
	}

	if err := store.CreateRun(&state.Run{ID: "without-plan"}); err != nil {
		t.Fatal(err)
	}
	c.runID = "without-plan"
	w := c.request(http.MethodGet, "", "admin", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"collections":[]`) {
		t.Fatalf("run without declarations: %d %s", w.Code, w.Body)
	}
}

func collectionResponse(t *testing.T, w *httptest.ResponseRecorder, status int) struct {
	Collection struct {
		state.RunCollection
		Open bool
	}
	Accepted bool
} {
	t.Helper()
	var result struct {
		Collection struct {
			state.RunCollection
			Open bool
		}
		Accepted bool
	}
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
	}
	if status == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func waitCollectionRun(t *testing.T, store state.Store, id, want string) *state.Run {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		run, err := store.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == want {
			return run
		}
		if run.State == plan.RunFailed {
			t.Fatalf("run failed: %s", run.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run did not reach %s", want)
	return nil
}

func TestAppendableCollectionRecovery(t *testing.T) {
	db := filepath.Join(t.TempDir(), "runs.db")
	open := func() *sqlite.Store {
		t.Helper()
		store, err := sqlite.Open(db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	executor := collectionExecutor{make(chan workers.ExecuteRequest, 8), make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	engine := plan.NewEngineWithStore(collectionPlan(6, 30), executor, store)
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{"items":[{"key":"a","n":9007199254740993}]}`))
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := store.GetRun(id)
	client := newCollectionClient(store, id)
	done := make(chan error, 1)
	go func() { done <- engine.Run(id) }()
	receive := func(key string) workers.ExecuteRequest {
		t.Helper()
		select {
		case req := <-executor.requests:
			if req.RunID != id || req.Inputs["value"].(map[string]any)["key"] != key {
				t.Fatalf("wrong execution for %s: %+v", key, req)
			}
			return req
		case <-time.After(5 * time.Second):
			t.Fatalf("item %s did not execute", key)
			return workers.ExecuteRequest{}
		}
	}
	activationA := receive("a").ActivationID
	appendItem := func(raw string, accepted bool) {
		t.Helper()
		if got := collectionResponse(t, client.call("POST", "/items", "inputs", raw), 200); got.Accepted != accepted {
			t.Fatalf("accepted=%v for %s", got.Accepted, raw)
		}
	}
	appendItem(`{"key":"b","n":2}`, true)
	appendItem(`{"n":2,"key":"b"}`, false)
	appendItem(`{"key":"a","n":9007199254740993}`, false)
	collectionResponse(t, client.call("POST", "/items", "inputs", `{"key":"b","n":3}`), 409)
	close(executor.release)
	receive("b")
	waitCollectionRun(t, store, id, plan.RunWaiting)
	appendItem(`{"key":"c","n":3}`, true)
	receive("c")
	waitCollectionRun(t, store, id, plan.RunWaiting)
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("coordinator shutdown: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	client = newCollectionClient(store, id)
	view := collectionResponse(t, client.call("GET", "", "read", ""), 200).Collection
	if !view.Open || view.IdleDeadline == nil || len(view.Items) != 3 {
		t.Fatalf("recovered collection: %+v", view)
	}
	// Commit an item while the interpreter is offline, then reopen storage
	// again to prove recovery drains a previously accepted pending item.
	appendItem(`{"key":"d","n":4}`, true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	client = newCollectionClient(store, id)
	engines, err := plan.RecoverRuns(store, executor)
	if err != nil || engines[id] == nil || len(engines) != 1 {
		t.Fatalf("recovered engines: %v, %v", engines, err)
	}
	go func() { done <- engines[id].Run(id) }()
	appendItem(`{"key":"c","n":3}`, false)
	appendItem(`{"key":"d","n":4}`, false)
	appendItem(`{"key":"e","n":5}`, true)
	collectionResponse(t, client.call("POST", "/close", "inputs", ""), 200)
	receive("d")
	receive("e")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed collection did not drain")
	}
	final := waitCollectionRun(t, store, id, plan.RunSucceeded)
	if !bytes.Equal(final.PlanJSON, initial.PlanJSON) || final.PlanDigest != initial.PlanDigest || !reflect.DeepEqual(final.Input, initial.Input) {
		t.Fatal("pinned plan or initial input changed")
	}
	output, _ := json.Marshal(final.Output)
	if string(output) != `[{"key":"a","n":9007199254740993},{"key":"b","n":2},{"key":"c","n":3},{"key":"d","n":4},{"key":"e","n":5}]` {
		t.Fatalf("results: %s", output)
	}
	act, err := store.GetActivation(activationA)
	if err != nil || act.State != plan.ActSucceeded || act.RunID != id {
		t.Fatalf("activation identity: %+v, %v", act, err)
	}
	appendItem(`{"key":"d","n":4}`, false)
	collectionResponse(t, client.call("POST", "/items", "inputs", `{"key":"f","n":6}`), 409)
	collectionResponse(t, client.call("POST", "/close", "inputs", ""), 200)
}

func TestAppendableCollectionValidationAndPermissions(t *testing.T) {
	store := state.New()
	engine := plan.NewEngineWithStore(collectionPlan(2, 30), workers.NewFake(), store)
	for _, input := range []string{
		`{"items":[{"key":"a","n":"bad"}]}`,
		`{"items":[{"key":"a","n":1},{"key":"a","n":2}]}`,
		`{"items":[{"key":"a","n":1},{"key":"b","n":2},{"key":"c","n":3}]}`,
	} {
		if _, err := engine.CreateRun(json.RawMessage(input)); err == nil {
			t.Errorf("invalid input accepted: %s", input)
		}
	}
	id, err := engine.CreateRun(json.RawMessage(`{"items":[{"key":"a","n":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCollectionClient(store, id)
	for _, tc := range []struct {
		method, suffix, token, body string
		status                      int
	}{
		{"GET", "", "", "", 401},
		{"GET", "", "other", "", 403},
		{"GET", "", "read", "", 200},
		{"GET", "", "inputs", "", 200},
		{"POST", "/items", "other", `{}`, 403},
		{"POST", "/items", "worker", `{}`, 403},
		{"POST", "/items", "read", `{}`, 403},
		{"POST", "/close", "read", "", 403},
		{"POST", "/items", "inputs", `{} {}`, 400},
		{"POST", "/items", "inputs", `{"n":2}`, 422},
		{"POST", "/items", "inputs", `{"key":"b","n":"bad"}`, 422},
		{"POST", "/items", "inputs", `{"key":"a","n":"different"}`, 409},
		{"POST", "/items", "inputs", `{"key":"b","n":2}`, 200},
		{"POST", "/items", "inputs", `{"key":"c","n":3}`, 409},
	} {
		if w := c.call(tc.method, tc.suffix, tc.token, tc.body); w.Code != tc.status {
			t.Errorf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	if err := store.SetRunState(id, plan.RunCancelled); err != nil {
		t.Fatal(err)
	}
	got := collectionResponse(t, c.call("GET", "", "read", ""), 200).Collection
	if got.Open || got.ClosedReason != "cancelled" || len(got.Items) != 2 {
		t.Fatalf("cancelled collection: %+v", got)
	}
	collectionResponse(t, c.call("POST", "/items", "inputs", `{"key":"c","n":3}`), 409)
	if collectionResponse(t, c.call("POST", "/items", "inputs", `{"key":"b","n":2}`), 200).Accepted {
		t.Fatal("cancellation changed the existing acceptance receipt")
	}
}

func TestAppendableCollectionResponseCapacity(t *testing.T) {
	store := state.New()
	p := collectionPlan(3, 30)
	p.Subgraphs["root"].Nodes[0].Appendable.ItemSchema = json.RawMessage(`{"type":"object","required":["key","data"]}`)
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	// Escaping expands these payloads when the collection is encoded for HTTP.
	data := strings.Repeat("<", 400_000)
	a := fmt.Sprintf(`{"key":"a","data":"%s"}`, data)
	b := fmt.Sprintf(`{"key":"b","data":"%s"}`, data)
	if _, err := engine.CreateRun(json.RawMessage(`{"items":[` + a + `,` + b + `]}`)); !errors.Is(err, state.ErrCollectionCapacity) {
		t.Fatalf("oversized seed: %v", err)
	}
	id, err := engine.CreateRun(json.RawMessage(`{"items":[` + a + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCollectionClient(store, id)
	response := c.call("POST", "/items", "inputs", b)
	collectionResponse(t, response, 409)
	if !strings.Contains(response.Body.String(), "MUXLIO_COLLECTION_CAPACITY") {
		t.Fatal(response.Body.String())
	}
	response = c.call("POST", "/items", "inputs", a)
	if receipt := collectionResponse(t, response, 200); receipt.Accepted || len(receipt.Collection.Items) != 1 {
		t.Fatal("unexpected receipt after rejecting the second item")
	}
	if response.Body.Len() >= 4<<20 {
		t.Fatalf("receipt exceeds MCP response budget: %d bytes", response.Body.Len())
	}
}

func TestAppendableCollectionWaitsAfterResume(t *testing.T) {
	store := state.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := plan.NewEngineWithStore(collectionPlan(2, 30), workers.NewFake(), store)
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run(id) }()
	waitCollectionRun(t, store, id, plan.RunWaiting)
	engine.Pause()
	if changed, err := store.TransitionRunState(id, []string{plan.RunWaiting}, plan.RunPaused); err != nil || !changed {
		t.Fatalf("pause transition: %v, %v", changed, err)
	}
	if changed, err := store.TransitionRunState(id, []string{plan.RunPaused}, plan.RunRunning); err != nil || !changed {
		t.Fatalf("resume transition: %v, %v", changed, err)
	}
	engine.Resume()
	waitCollectionRun(t, store, id, plan.RunWaiting)
	c := newCollectionClient(store, id)
	collectionResponse(t, c.call("POST", "/close", "inputs", ""), 200)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed collection did not close")
	}
	waitCollectionRun(t, store, id, plan.RunSucceeded)
}

func TestAppendableCollectionConcurrentClose(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executor := workers.NewFake().ByNode("echo", workers.ExecuteResult{Outputs: map[string]any{"value": "ok"}})
	engine := plan.NewEngineWithStore(collectionPlan(32, 30), executor, store)
	id, err := engine.CreateRun(json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCollectionClient(store, id)
	start := make(chan struct{})
	statuses := make([]int, 16)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			<-start
			statuses[i] = c.call("POST", "/items", "inputs", fmt.Sprintf(`{"key":"%d","n":%d}`, i, i)).Code
		})
	}
	close(start)
	collectionResponse(t, c.call("POST", "/close", "inputs", ""), 200)
	wg.Wait()
	accepted := map[string]bool{}
	for i, status := range statuses {
		switch status {
		case 200:
			accepted[fmt.Sprint(i)] = true
		case 409:
		default:
			t.Errorf("append %d: unexpected status %d", i, status)
		}
	}
	got := collectionResponse(t, c.call("GET", "", "inputs", ""), 200).Collection
	if got.Open || len(got.Items) != len(accepted) {
		t.Fatalf("accepted %v, stored %+v", accepted, got)
	}
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	calls := executor.Calls()
	if len(calls) != len(got.Items) {
		t.Fatalf("ran %d of %d items", len(calls), len(got.Items))
	}
	for i, item := range got.Items {
		if !accepted[item.ID] || calls[i].Inputs["value"].(map[string]any)["key"] != item.ID {
			t.Fatalf("unexpected item at %d: %+v", i, item)
		}
	}
}

func TestAppendableCollectionIdleTimeoutAndFixedInput(t *testing.T) {
	store := state.New()
	p := collectionPlan(2, 0.2)
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	id, err := engine.CreateRun(json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCollectionClient(store, id)
	done := make(chan error, 1)
	go func() { done <- engine.Run(id) }()
	waitCollectionRun(t, store, id, plan.RunWaiting)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle collection did not complete")
	}
	got := collectionResponse(t, c.call("GET", "", "read", ""), 200).Collection
	if got.Open || got.ClosedReason != "idle_timeout" {
		t.Fatalf("idle collection: %+v", got)
	}
	collectionResponse(t, c.call("POST", "/items", "inputs", `{"key":"late","n":1}`), 409)
	waitCollectionRun(t, store, id, plan.RunSucceeded)

	p = collectionPlan(2, 1)
	p.Subgraphs["root"].Nodes[0].Appendable = nil
	engine = plan.NewEngineWithStore(p, workers.NewFake(), store)
	id, err = engine.CreateRun(json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	waitCollectionRun(t, store, id, plan.RunSucceeded)
	collectionResponse(t, newCollectionClient(store, id).call("GET", "", "read", ""), 404)
}
