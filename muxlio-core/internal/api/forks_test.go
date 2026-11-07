package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestForkAndCompareAPI(t *testing.T) {
	s := state.New()
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"fork","root":"root","subgraphs":{"root":{"nodes":[]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	e := plan.NewEngineWithStore(&p, nil, s)
	id, err := e.CreateRun(json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(id); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(s, nil)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	w := call("POST", "/v1/runs/"+id+"/fork", `{"input":{"n":2}}`)
	if w.Code != 201 {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		RunID   string `json:"runId"`
		Started bool   `json:"started"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetRun(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.State != "created" || fork.Fork.SourceRunID != id || created.Started {
		t.Fatalf("fork: %+v", fork)
	}
	w = call("GET", "/v1/runs/"+id+"/compare?other="+created.RunID, "")
	var c state.RunComparison
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !c.InputsChanged || c.PlanChanged || !c.ResultsChanged {
		t.Fatalf("compare: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v1/runs/missing/fork", `{}`, 404},
		{"/v1/runs/" + id + "/fork", `{"start":true}`, 503},
		{"/v1/runs/" + id + "/fork", `{"unknown":true}`, 400},
		{"/v1/runs/" + id + "/fork", `{} {}`, 400},
		{"/v1/runs/" + id + "/fork", `{"planDigest":"missing"}`, 404},
		{"/v1/runs/" + id + "/fork", `{"input":`, 400},
		{"/v1/runs/" + id + "/fork", `{"input":"` + strings.Repeat("x", 8<<20) + `"}`, 400},
	} {
		if got := call("POST", tc.path, tc.body); got.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, got.Code, got.Body.String())
		}
	}
	if got := call("GET", "/v1/runs/"+id+"/compare", ""); got.Code != 400 {
		t.Fatal("missing comparison target accepted")
	}
	if got := call("GET", "/v1/runs/"+id+"/compare?other=missing", ""); got.Code != 404 {
		t.Fatal("missing run accepted")
	}
}

func TestRunGrantCannotForkOrCompareOtherHistory(t *testing.T) {
	grant := AccessGrant{RunID: "own", Permissions: []string{"read", "control"}}
	for _, r := range []*struct{ method, path string }{
		{"POST", "/v1/runs/own/fork"},
		{"GET", "/v1/runs/own/compare?other=secret"},
		{"GET", "/v1/runs/own/compare"},
	} {
		if grant.authorize(httptest.NewRequest(r.method, r.path, nil)) {
			t.Fatalf("run-scoped grant authorized %s", r.path)
		}
	}
}

type forkStartRunner struct {
	stubRunner
	started []string
}

func (r *forkStartRunner) StartPersistedRun(id string) error {
	r.started = append(r.started, id)
	return nil
}

func TestForkStartIsExplicit(t *testing.T) {
	s := state.New()
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"fork","root":"root","subgraphs":{"root":{"nodes":[]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	e := plan.NewEngineWithStore(&p, nil, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(id); err != nil {
		t.Fatal(err)
	}
	source, err := s.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkflowVersion(source.PlanDigest, source.PlanJSON); err != nil {
		t.Fatal(err)
	}
	runner := &forkStartRunner{}
	h := NewRouter(s, runner)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/runs/"+id+"/fork", strings.NewReader(`{"planDigest":"`+source.PlanDigest+`"}`)))
	if w.Code != 201 || len(runner.started) != 0 {
		t.Fatalf("creation started execution: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/runs/"+created.RunID+"/start", nil))
	if w.Code != 202 || len(runner.started) != 1 || runner.started[0] != created.RunID {
		t.Fatalf("explicit start: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/runs/"+id+"/start", nil))
	if w.Code != 409 {
		t.Fatal("terminal run started")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/runs/"+id+"/fork", strings.NewReader(`{"start":true}`)))
	if w.Code != 201 || len(runner.started) != 2 {
		t.Fatalf("start at creation: %d %s", w.Code, w.Body.String())
	}
}
