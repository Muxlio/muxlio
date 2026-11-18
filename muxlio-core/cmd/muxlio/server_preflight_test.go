package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestServerPreflightThenSubmit(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := api.NewCoordinator(store)
	c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "local", Path: t.TempDir(), Aliases: []string{"alias"}}}
	if _, err := c.Register(api.WorkerRegistration{WorkerID: "local", Capacity: 1, Capabilities: []api.WorkerCapability{{Name: "test"}}, Labels: map[string]string{workers.WorkspaceIdentityLabelPrefix + "project": "true"}}); err != nil {
		t.Fatal(err)
	}
	r := &serverRunner{store: store, exec: c, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: ctx}
	t.Cleanup(func() { cancel(); r.wg.Wait(); store.Close() })
	handler := api.NewRouter(store, r)
	p := strings.ReplaceAll(submissionPlan, `"workspaceMode":"empty"`, `"workspaceMode":"snapshot"`)
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return w
	}
	for _, tc := range []struct {
		id       string
		feasible bool
	}{
		{"unregistered", false}, {"alias", true},
	} {
		body := `{"workspaceId":"` + tc.id + `","plan":` + p + `,"input":{}}`
		w := call("/v1/runs/preflight", body)
		var resp api.PreflightResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != 200 || resp.Feasible != tc.feasible {
			t.Fatalf("preflight: %d %s (%v)", w.Code, w.Body, err)
		}
		if tc.feasible && resp.WorkspaceID != "project" {
			t.Fatalf("alias unresolved: %+v", resp)
		}
		if len(r.engines) != 0 || len(r.workers) != 0 {
			t.Fatal("preflight created execution state")
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/runs", nil))
		var listing struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || len(listing.Runs) != 0 {
			t.Fatalf("preflight created runs: %s (%v)", w.Body, err)
		}
	}
	// Omitted input has the same effective value in preflight and submission.
	if w := call("/v1/runs", `{"workspaceId":"alias","plan":`+p+`}`); w.Code != http.StatusCreated {
		t.Fatalf("clean preflight cannot submit: %d %s", w.Code, w.Body)
	}
}

func TestWorkspaceRunErrorsKeepLocalPathsPrivate(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	privatePath := filepath.Join(t.TempDir(), "private checkout")
	c := api.NewCoordinator(store)
	c.Workspace = privatePath
	r := &serverRunner{store: store, exec: c, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: ctx}
	t.Cleanup(func() { cancel(); r.wg.Wait(); store.Close() })
	router := api.NewRouter(store, r)

	var p plan.Plan
	if err := json.Unmarshal([]byte(strings.ReplaceAll(submissionPlan, `"workspaceMode":"empty"`, `"workspaceMode":"snapshot"`)), &p); err != nil {
		t.Fatal(err)
	}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowVersion(digest, compiled); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		body string
		want int
	}{
		{"/v1/runs/preflight", `{"workspaceId":"default","plan":` + string(compiled) + `}`, http.StatusOK},
		{"/v1/runs", `{"workspaceId":"default","planDigest":"` + digest + `"}`, http.StatusBadRequest},
	} {
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
		if body := resp.Body.String(); resp.Code != tc.want ||
			strings.Contains(body, privatePath) || !strings.Contains(body, "could not be prepared") {
			t.Errorf("%s returned HTTP %d: %s", tc.path, resp.Code, body)
		}
	}
}
