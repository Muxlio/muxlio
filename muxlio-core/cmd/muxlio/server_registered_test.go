package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestRegisteredSubmissionAuthenticatedReusableAndPersistent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runs.db")
	st, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := t.TempDir()
	newRunner := func() *serverRunner {
		c := api.NewCoordinator(st)
		c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "local", Path: workspace, Aliases: []string{"alias"}}, "other": {WorkerID: "local", Path: workspace}}
		return &serverRunner{store: st, exec: c, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	}
	runner := newRunner()
	handler := api.WithAuth(api.NewRouter(st, runner), "client", "worker")
	request := func(method, path, body, token, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	raw := `{"irVersion":"1.0","name":"typed","root":"root","inputSchema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]},"subgraphs":{"root":{"nodes":[]}}}`
	w := request("POST", "/v1/workflow-versions", raw, "client", "")
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var version map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &version)
	digest := version["digest"]
	pinned, err := st.GetWorkflowVersion(digest)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"planDigest":"` + digest + `","workspaceId":"alias","input":{"n":9007199254740993}}`
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"worker", 403}} {
		w = request("POST", "/v1/runs", body, tc.token, "key")
		if w.Code != tc.status {
			t.Fatalf("auth: %d %s", w.Code, w.Body.String())
		}
	}
	w = request("POST", "/v1/runs", body, "client", "key")
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var response map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	id := response["runId"]
	runner.wg.Wait()
	run, err := st.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(run.PlanJSON, pinned) || run.PlanDigest != digest || run.WorkspaceID != "project" {
		t.Fatalf("registered bytes changed: %+v", run)
	}
	if run.Input.(map[string]any)["n"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("rounded input: %v", run.Input)
	}
	// A fresh client can reference the same definition without resending it.
	w = request("POST", "/v1/runs", body, "client", "another-invocation")
	if w.Code != 201 {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	runner.wg.Wait()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{strings.Replace(body, "alias", "project", 1), 200},
		{strings.Replace(body, "9007199254740993", "9007199254740992", 1), 409},
		{strings.Replace(body, "alias", "unknown", 1), 400},
		{strings.Replace(body, "9007199254740993", `"wrong"`, 1), 400},
	} {
		w = request("POST", "/v1/runs", tc.body, "client", "key")
		if w.Code != tc.status {
			t.Fatalf("retry/reject: %d %s", w.Code, w.Body.String())
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	runner = newRunner()
	handler = api.WithAuth(api.NewRouter(st, runner), "client", "worker")
	w = request("POST", "/v1/runs", body, "client", "key")
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 || response["runId"] != id || len(runner.engines) != 0 {
		t.Fatalf("restart: %d %s", w.Code, w.Body.String())
	}
	w = request("DELETE", "/v1/workflow-versions/"+digest, "", "worker", "")
	if w.Code != 403 {
		t.Fatalf("retire auth: %d", w.Code)
	}
	w = request("DELETE", "/v1/workflow-versions/"+digest, "", "client", "")
	if w.Code != 200 {
		t.Fatalf("retire: %d %s", w.Code, w.Body.String())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	handler = api.WithAuth(api.NewRouter(st, newRunner()), "client", "worker")
	w = request("POST", "/v1/runs", body, "client", "new-key")
	if w.Code != 410 {
		t.Fatalf("retired after restart: %d %s", w.Code, w.Body.String())
	}
	if _, err := st.GetWorkflowVersion(digest); !errors.Is(err, state.ErrWorkflowVersionRetired) {
		t.Fatalf("retirement missing: %v", err)
	}
	versions, err := st.ListWorkflowVersions()
	if err != nil || len(versions) != 1 || !versions[0].Retired {
		t.Fatalf("list: %+v %v", versions, err)
	}
	run, err = st.GetRun(id)
	if err != nil || !bytes.Equal(run.PlanJSON, pinned) {
		t.Fatalf("retirement changed existing run: %+v %v", run, err)
	}
}

func TestRegisteredSubmissionCannotOverridePlanWorkspace(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := api.NewCoordinator(st)
	c.Workspaces = workers.WorkspaceRegistry{"first": {WorkerID: "local", Path: t.TempDir(), Aliases: []string{"alias"}}, "second": {WorkerID: "local", Path: t.TempDir()}}
	runner := &serverRunner{store: st, exec: c, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"fixed","workspaceId":"alias","root":"root","subgraphs":{"root":{"nodes":[]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(&p)
	if _, _, err := runner.CreateRegisteredRun(raw, json.RawMessage(`{}`), "second", "bad"); err == nil {
		t.Fatal("overrode fixed workspace")
	}
	id, dup, err := runner.CreateRegisteredRun(raw, json.RawMessage(`{}`), "first", "good")
	if err != nil || dup {
		t.Fatalf("canonical alias: %v %v", dup, err)
	}
	if err := runner.Run(id); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(id)
	if err != nil || !bytes.Equal(run.PlanJSON, raw) || run.WorkspaceID != "first" {
		t.Fatalf("pin/workspace: %+v %v", run, err)
	}
}
