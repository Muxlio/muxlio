package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestSchedulesAPI(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	router := NewRouter(s, nil)
	request := func(method, path string, body []byte, status int) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(body)))
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	p := &plan.Plan{Name: "scheduled", IRVersion: "1.0", Root: "root", InputSchema: json.RawMessage(`{"type":"boolean"}`), Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/task"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	request(http.MethodPost, "/v1/workflow-versions", raw, http.StatusCreated)
	d := schedule.Definition{ID: "daily", WorkflowDigest: digest, Input: json.RawMessage(`false`), Kind: "cron", StartAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Cron: "30 1 * * *", Timezone: "America/New_York", Overlap: "allow", MaxCatchUp: 2}
	raw, _ = json.Marshal(d)
	request(http.MethodPost, "/v1/schedules", raw, http.StatusCreated)
	request(http.MethodPost, "/v1/schedules", raw, http.StatusBadRequest)
	request(http.MethodPost, "/v1/schedules", append(raw, []byte(` {}`)...), http.StatusBadRequest)
	request(http.MethodPost, "/v1/schedules", []byte(`{"unknown":true}`), http.StatusBadRequest)
	request(http.MethodPut, "/v1/schedules", nil, http.StatusMethodNotAllowed)
	request(http.MethodGet, "/v1/schedules/missing/fires", nil, http.StatusNotFound)
	request(http.MethodGet, "/v1/schedules/daily", nil, http.StatusNotFound)
	var listed struct {
		Schedules []schedule.Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(request(http.MethodGet, "/v1/schedules", nil, 200), &listed); err != nil || len(listed.Schedules) != 1 {
		t.Fatalf("list=%+v %v", listed, err)
	}
	now := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	if ids, err := s.TickSchedules(now); err != nil || len(ids) != 2 {
		t.Fatalf("DST repeat lost: %v %v", ids, err)
	}
	var history struct {
		Fires []schedule.Fire `json:"fires"`
	}
	if err := json.Unmarshal(request(http.MethodGet, "/v1/schedules/daily/fires", nil, 200), &history); err != nil || len(history.Fires) != 2 {
		t.Fatalf("history=%+v %v", history, err)
	}
	if !history.Fires[1].DueAt.Equal(history.Fires[0].DueAt.Add(time.Hour)) {
		t.Fatalf("DST identities=%+v", history)
	}
	d.ID = "bad-input"
	d.Input = json.RawMessage(`0`)
	raw, _ = json.Marshal(d)
	request(http.MethodPost, "/v1/schedules", raw, 400)
}

func TestSchedulesRequireAdministrator(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	handler := WithAuth(NewRouter(s, nil), "admin-token", "worker-token",
		AccessGrant{Token: "run-token", RunID: "run-1", Permissions: []string{"read", "control"}},
		AccessGrant{Token: "scoped-worker-token", WorkerID: "worker-1", Permissions: []string{"worker"}},
	)
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/v1/schedules"},
		{http.MethodPost, "/v1/schedules"},
		{http.MethodGet, "/v1/schedules/missing/fires"},
	} {
		for _, token := range []string{"", "worker-token", "run-token", "scoped-worker-token", "admin-token"} {
			t.Run(endpoint.method+endpoint.path+"/"+token, func(t *testing.T) {
				req := httptest.NewRequest(endpoint.method, endpoint.path, bytes.NewBufferString(`{}`))
				req.RemoteAddr = "192.0.2.1:1234"
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				want := http.StatusForbidden
				if token == "" {
					want = http.StatusUnauthorized
				} else if token == "admin-token" {
					switch {
					case endpoint.method == http.MethodPost:
						want = http.StatusBadRequest
					case endpoint.path == "/v1/schedules":
						want = http.StatusOK
					default:
						want = http.StatusNotFound
					}
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
				}
			})
		}
	}
}
