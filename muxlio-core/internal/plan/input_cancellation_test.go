package plan_test

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

type gatedInputCreation struct {
	state.Store
	entered, release, created, finish chan struct{}
}

func (s *gatedInputCreation) CreateInputRequest(q *state.InputRequest) error {
	close(s.entered)
	<-s.release
	err := s.Store.CreateInputRequest(q)
	close(s.created)
	<-s.finish
	return err
}

func TestCancelDuringInputCreation(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var base state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "race.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				base = db
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			await := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for engine")
				}
			}
			gate := &gatedInputCreation{Store: base, entered: make(chan struct{}), release: make(chan struct{}), created: make(chan struct{}), finish: make(chan struct{})}
			p := &plan.Plan{IRVersion: "1.0", Name: "input", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "ask", Kind: plan.KindWait, WaitFor: "input", SignalName: "approval", RequestSchema: json.RawMessage(`{"type":"boolean"}`), RequestPermissions: []string{"approve"}}}}}}
			e := plan.NewEngineWithStore(p, nil, gate)
			id, err := e.CreateRun(json.RawMessage(`{}`))
			must(err)
			done := make(chan struct{})
			var releaseOnce, finishOnce sync.Once
			defer func() {
				e.Cancel()
				releaseOnce.Do(func() { close(gate.release) })
				finishOnce.Do(func() { close(gate.finish) })
				await(done)
			}()
			go func() { defer close(done); _ = e.Run(id) }()
			await(gate.entered)
			// Exact server cancellation order, while creation is paused before insertion.
			changed, err := base.TransitionRunState(id, []string{plan.RunRunning, plan.RunWaiting}, plan.RunCancelled)
			must(err)
			if !changed {
				t.Fatal("cancel rejected")
			}
			must(base.CancelRunWork(id))
			e.Cancel()
			releaseOnce.Do(func() { close(gate.release) })
			await(gate.created)
			qs, err := base.ListInputRequests(id)
			must(err)
			if len(qs) != 1 {
				t.Fatalf("requests: %+v", qs)
			}
			old := qs[0]
			h := api.WithAuth(api.NewRouter(base, nil), "admin", "worker", api.AccessGrant{Token: "limited", RunID: id, Permissions: []string{"inputs"}})
			call := func(requestID, token, body, key string, want int) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest("POST", "/v1/runs/"+id+"/input-requests/"+requestID, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Idempotency-Key", key)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("input response: %d want %d: %s", w.Code, want, w.Body.String())
				}
				return w
			}
			// The old engine remains paused after insertion, before its cleanup can help.
			call(old.RequestID, "admin", `true`, "stale", 409)
			if old.State != state.InputRequestCancelled {
				t.Fatalf("late request remains open: %+v", old)
			}
			finishOnce.Do(func() { close(gate.finish) })
			await(done)
			fresh := plan.NewEngineWithStore(nil, nil, base)
			must(fresh.Retry(id))
			qs, err = base.ListInputRequests(id)
			must(err)
			if len(qs) != 2 {
				t.Fatalf("requests after retry: %+v", qs)
			}
			var renewed state.InputRequest
			for _, q := range qs {
				if q.State == state.InputRequestPending {
					renewed = q
				}
			}
			if renewed.RequestID == "" || renewed.RequestID == old.RequestID || string(renewed.Schema) != string(old.Schema) || len(renewed.Permissions) != 1 || renewed.Permissions[0] != "approve" {
				t.Fatalf("missing renewed contract: %+v", renewed)
			}
			call(old.RequestID, "admin", `true`, "stale", 409)
			call(renewed.RequestID, "limited", `true`, "fresh", 403)
			call(renewed.RequestID, "admin", `"wrong"`, "fresh", 422)
			w := call(renewed.RequestID, "admin", `false`, "fresh", 200)
			if !strings.Contains(w.Body.String(), `"accepted":true`) {
				t.Fatal(w.Body.String())
			}
			must(fresh.Run(id))
			run, err := base.GetRun(id)
			must(err)
			if run.State != plan.RunSucceeded {
				t.Fatalf("run: %+v", run)
			}
			w = call(renewed.RequestID, "admin", `false`, "fresh", 200)
			if !strings.Contains(w.Body.String(), `"accepted":false`) {
				t.Fatal(w.Body.String())
			}
			call(old.RequestID, "admin", `true`, "stale", 409)
		})
	}
}
