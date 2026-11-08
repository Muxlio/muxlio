package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestCancelledInputRetryHTTP(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "inputs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &plan.Plan{IRVersion: "1.0", Name: "input", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "ask", Kind: plan.KindWait, WaitFor: "input", SignalName: "approval", RequestSchema: json.RawMessage(`{"type":"boolean"}`), RequestPermissions: []string{"approve"}}}}}}
	e := plan.NewEngineWithStore(p, nil, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &serverRunner{store: s, ctx: ctx, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	defer func() { cancel(); runner.wg.Wait() }()
	h := api.WithAuth(api.NewRouter(s, runner), "admin", "worker", api.AccessGrant{Token: "limited", RunID: id, Permissions: []string{"inputs"}})
	call := func(path, body, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/runs/"+id+"/"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "answer")
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	waitPending := func(previous string) state.InputRequest {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			qs, err := s.ListInputRequests(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range qs {
				if q.State == state.InputRequestPending && q.RequestID != previous {
					return q
				}
			}
			run, _ := s.GetRun(id)
			if run.State == plan.RunFailed {
				t.Fatalf("retry failed: %s", run.Error)
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("no renewed pending input request")
		return state.InputRequest{}
	}
	call("start", `{}`, "admin", 202)
	old := waitPending("")
	// Repeat cancellation to exercise renewal of an already renewed request.
	for i := 0; i < 2; i++ {
		call("cancel", `{}`, "admin", 200)
		runner.wg.Wait()
		call("retry", `{}`, "admin", 200)
		renewed := waitPending(old.RequestID)
		if string(renewed.Schema) != string(old.Schema) || len(renewed.Permissions) != 1 || renewed.Permissions[0] != "approve" {
			t.Fatalf("lost request contract: %+v", renewed)
		}
		call("input-requests/"+old.RequestID, `true`, "admin", 409)
		old = renewed
	}
	call("input-requests/"+old.RequestID, `true`, "limited", 403)
	call("input-requests/"+old.RequestID, `"wrong"`, "admin", 422)
	w := call("input-requests/"+old.RequestID, `true`, "admin", 200)
	if !strings.Contains(w.Body.String(), `"accepted":true`) {
		t.Fatal(w.Body.String())
	}
	runner.wg.Wait()
	run, err := s.GetRun(id)
	if err != nil || run.State != plan.RunSucceeded {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	w = call("input-requests/"+old.RequestID, `true`, "admin", 200)
	if !strings.Contains(w.Body.String(), `"accepted":false`) {
		t.Fatal(w.Body.String())
	}
}
