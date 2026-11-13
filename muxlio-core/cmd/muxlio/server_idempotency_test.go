package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

const submissionPlan = `{"irVersion":"1.0","name":"submit","root":"root","subgraphs":{"root":{"nodes":[{"id":"effect","kind":"execute","executor":"test","workspaceMode":"empty"}]}}}`

func TestIdempotentSubmissionHTTPConcurrentRestart(t *testing.T) {
	for _, mode := range []string{"full-plan", "registered"} {
		t.Run(mode, func(t *testing.T) {
			testIdempotentSubmissionHTTPConcurrentRestart(t, mode == "registered")
		})
	}
}

func testIdempotentSubmissionHTTPConcurrentRestart(t *testing.T, registered bool) {
	path := filepath.Join(t.TempDir(), "runs.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	exec := workers.NewFake()
	runner := &serverRunner{store: st, exec: exec, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	handler := api.NewRouter(st, runner)
	body := `{"plan":` + submissionPlan + `,"input":{"n":9007199254740993}}`
	if registered {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", strings.NewReader(submissionPlan)))
		if w.Code != 201 {
			t.Fatalf("register: %d %s", w.Code, w.Body.String())
		}
		var version map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &version)
		body = `{"planDigest":"` + version["digest"] + `","input":{"n":9007199254740993}}`
	}

	submit := func(h http.Handler, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(payload))
		req.Header.Set("Idempotency-Key", "invocation")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- submit(handler, body) }()
	}
	wg.Wait()
	close(responses)
	var id string
	created := 0
	for w := range responses {
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("submission: %d %s", w.Code, w.Body.String())
		}
		if w.Code == 201 {
			created++
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = resp["runId"]
		}
		if id == "" || resp["runId"] != id {
			t.Fatalf("different IDs: %s %v", id, resp)
		}
	}
	runner.wg.Wait()
	if created != 1 || len(exec.Calls()) != 1 {
		t.Fatalf("created=%d effects=%d", created, len(exec.Calls()))
	}
	run, err := st.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Input.(map[string]any)["n"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("rounded input: %v", run.Input)
	}
	if w := submit(handler, strings.Replace(body, "9007199254740993", "9007199254740992", 1)); w.Code != 409 {
		t.Fatalf("different exact input: %d %s", w.Code, w.Body.String())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runner = &serverRunner{store: st, exec: exec, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	// The client discarded all original responses. Retry after reopening the DB.
	w := submit(api.NewRouter(st, runner), body)
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != 200 || resp["runId"] != id || resp["state"] != plan.RunSucceeded || len(exec.Calls()) != 1 || len(runner.engines) != 0 {
		t.Fatalf("restart replay: %d %s effects=%d", w.Code, w.Body.String(), len(exec.Calls()))
	}
}

func TestIdempotentSubmissionWorkspaceAuthorization(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := api.NewCoordinator(st)
	c.Workspaces = workers.WorkspaceRegistry{"first": {WorkerID: "local", Path: t.TempDir(), Aliases: []string{"alias"}}, "second": {WorkerID: "local", Path: t.TempDir()}}
	runner := &serverRunner{store: st, exec: c, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	p := func() *plan.Plan {
		var p plan.Plan
		if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"workspace","root":"root","subgraphs":{"root":{"nodes":[]}}}`), &p); err != nil {
			t.Fatal(err)
		}
		return &p
	}
	for _, workspace := range []string{"unknown", c.Workspaces["first"].Path} {
		if _, _, err := runner.CreateIdempotentRun(p(), json.RawMessage(`{}`), workspace, "key"); err == nil {
			t.Fatalf("unauthorized selector %s accepted", workspace)
		}
	}
	id, dup, err := runner.CreateIdempotentRun(p(), json.RawMessage(`{}`), "alias", "key")
	if err != nil || dup {
		t.Fatalf("alias: %v %v", dup, err)
	}
	retry, dup, err := runner.CreateIdempotentRun(p(), json.RawMessage(`{}`), "first", "key")
	if err != nil || !dup || retry != id {
		t.Fatalf("canonical retry: %s %v %v", retry, dup, err)
	}
	if err := runner.Run(id); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(id)
	if err != nil || run.WorkspaceID != "first" {
		t.Fatalf("workspace not pinned: %+v %v", run, err)
	}
	// Workspace inputs still require authorization when an explicit selector is supplied.
	var workspacePlan plan.Plan
	if err := json.Unmarshal([]byte(strings.Replace(submissionPlan, `,"workspaceMode":"empty"`, "", 1)), &workspacePlan); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"workspace": c.Workspaces["second"].Path})
	if _, _, err := runner.CreateIdempotentRun(&workspacePlan, input, "first", "outside"); err == nil {
		t.Fatal("input escaped authorized workspace")
	}
	bad := p()
	bad.Digest = "sha256:wrong"
	if _, _, err := runner.CreateIdempotentRun(bad, json.RawMessage(`{}`), "first", "bad"); err == nil {
		t.Fatal("invalid declared digest accepted")
	}
}

// A process may die after the atomic commit but before registering an engine.
func TestIdempotentSubmissionRecoveryBeforeDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var p plan.Plan
	if err := json.Unmarshal([]byte(submissionPlan), &p); err != nil {
		t.Fatal(err)
	}
	exec := workers.NewFake()
	input := json.RawMessage(`{}`)
	id, dup, err := plan.NewEngineWithStore(&p, exec, st).CreateRunIdempotent(input, "invocation", fmt.Sprintf("%x", sha256.Sum256(input)))
	if err != nil || dup {
		t.Fatalf("commit: %s %v %v", id, dup, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recovered, err := plan.RecoverRuns(st, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[id] == nil {
		t.Fatalf("recovery omitted committed run: %v", recovered)
	}
	runner := &serverRunner{store: st, exec: exec, ctx: context.Background(), engines: recovered, workers: map[string]chan struct{}{id: make(chan struct{})}}
	runner.wg.Add(1)
	retry, dup, err := runner.CreateIdempotentRun(&p, input, "", "invocation")
	if err != nil || !dup || retry != id || len(exec.Calls()) != 0 {
		t.Fatalf("retry before recovery: %s %v %v", retry, dup, err)
	}
	if err := runner.Run(id); err != nil {
		t.Fatal(err)
	}
	runner.wg.Wait()
	if len(exec.Calls()) != 1 {
		t.Fatalf("effects=%d", len(exec.Calls()))
	}
}
