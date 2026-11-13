package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestClientsSelectAuthorizedWorkspacesPerRun(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := api.NewCoordinator(store)
	c.Workspaces = workers.WorkspaceRegistry{
		"first":  {WorkerID: "local", Path: t.TempDir(), Aliases: []string{"client-one", "client-two"}},
		"second": {WorkerID: "local", Path: t.TempDir()},
		"remote": {WorkerID: "remote", Path: "/only/on/another/machine"},
	}
	labels := map[string]string{workers.WorkspaceIdentityLabelPrefix + "first": "true", workers.WorkspaceIdentityLabelPrefix + "second": "true"}
	if _, err := c.Register(api.WorkerRegistration{WorkerID: "local", Capacity: 1, Labels: labels, Capabilities: []api.WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	r := &serverRunner{store: store, exec: c, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: ctx}
	t.Cleanup(func() { cancel(); r.wg.Wait(); store.Close() })
	handler := api.NewRouter(store, r)
	p := json.RawMessage(`{"irVersion":"1.0","name":"workspace","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"test"}]}}}`)
	submit := func(id string, input any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"workspaceId": id, "plan": p, "input": input})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body)))
		return response
	}
	for _, id := range []string{"first", "second", "client-one", "client-two"} {
		// The plan and submission may use different aliases of one identity.
		p = json.RawMessage(`{"irVersion":"1.0","workspaceId":"first","name":"workspace","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"test"}]}}}`)
		if id == "second" {
			p = bytes.Replace(p, []byte(`"workspaceId":"first"`), []byte(`"workspaceId":"second"`), 1)
		}
		response := submit(id, map[string]any{})
		if response.Code != http.StatusCreated {
			t.Fatalf("submit %s: %d %s", id, response.Code, response.Body.String())
		}
		var created struct {
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		wantID := id
		if id == "client-one" || id == "client-two" {
			wantID = "first"
		}
		run, err := store.GetRun(created.RunID)
		if err != nil || run.WorkspaceID != wantID {
			t.Fatalf("unpinned workspace: %+v %v", run, err)
		}
		var assignment *api.WorkerAssignment
		deadline := time.Now().Add(3 * time.Second)
		for assignment == nil && time.Now().Before(deadline) {
			assignment, err = c.NextAssignment("local")
			if err != nil {
				t.Fatal(err)
			}
			if assignment == nil {
				time.Sleep(time.Millisecond)
			}
		}
		if assignment == nil {
			t.Fatal("workspace dispatch unavailable")
		}
		var req workers.ExecuteRequest
		if err := json.Unmarshal(assignment.Request, &req); err != nil {
			t.Fatal(err)
		}
		if req.WorkspaceID != wantID || req.WorkspaceWorkerID != "local" || req.Workspace != "" {
			t.Fatalf("wrong dispatch identity: %+v", req)
		}
		bindings, err := c.Workspaces.LocalBindings("local")
		if err != nil {
			t.Fatal(err)
		}
		bound, err := workers.ApplyAuthorizedWorkspace(req, "local", bindings, "")
		if err != nil || bound.Workspace != bindings[wantID].Path {
			t.Fatalf("wrong checkout: %+v %v", bound, err)
		}
		if err := c.Complete("local", api.WorkerCompletion{AssignmentID: assignment.AssignmentID, RunID: assignment.RunID, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		attempts, err := store.ListExecutionAttempts(created.RunID, "")
		if err != nil || len(attempts) != 1 || attempts[0].WorkspaceID != wantID {
			t.Fatalf("attempt workspace missing: %+v %v", attempts, err)
		}
	}
	if response := submit("second", map[string]any{}); response.Code != http.StatusBadRequest {
		t.Fatalf("conflicting workspace IDs accepted: %d %s", response.Code, response.Body.String())
	}
	p = bytes.Replace(p, []byte(`"workspaceId":"first",`), nil, 1)
	for _, id := range []string{"", "unknown-client-project", "remote", c.Workspaces["first"].Path} {
		if response := submit(id, map[string]any{}); response.Code != http.StatusBadRequest {
			t.Fatalf("unauthorized ID %q accepted: %d %s", id, response.Code, response.Body.String())
		}
	}
	if response := submit("first", map[string]any{"workspace": c.Workspaces["second"].Path}); response.Code != http.StatusBadRequest {
		t.Fatalf("unauthorized path accepted: %d %s", response.Code, response.Body.String())
	}
}
