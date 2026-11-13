package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestRunDiscoveryFiltersBeforePagination(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var st state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "runs.db"))
				if err != nil {
					t.Fatal(err)
				}
				st = db
			}
			defer st.Close()
			now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
			for _, fixture := range []struct {
				id, workspace, digest, caller, status string
				created                               time.Time
			}{
				{"legacy", "", "", "", "succeeded", now},
				{"a", "project", "sha256:one", "alice", "running", now},
				{"b", "project", "sha256:one", "alice", "succeeded", now.Add(time.Nanosecond)},
				{"c", "project", "sha256:two", "bob", "running", now.Add(time.Hour)},
				{"other", "different", "sha256:one", "alice", "running", now.Add(time.Hour)},
			} {
				run := &state.Run{ID: fixture.id, WorkspaceID: fixture.workspace, PlanDigest: fixture.digest, CreatedAt: fixture.created, SubmissionMetadata: state.SubmissionMetadata{OriginLabel: "Editor <local>", CallerID: fixture.caller}}
				if err := st.CreateRun(run); err != nil {
					t.Fatal(err)
				}
				if err := st.SetRunState(run.ID, fixture.status); err != nil {
					t.Fatal(err)
				}
			}
			h := NewRouter(st, nil)
			get := func(query string, caller Caller, code int) map[string]json.RawMessage {
				t.Helper()
				w := httptest.NewRecorder()
				r := withCaller(httptest.NewRequest("GET", "/v1/runs"+query, nil), caller)
				h.ServeHTTP(w, r)
				if w.Code != code {
					t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
				}
				var result map[string]json.RawMessage
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				return result
			}
			ids := func(result map[string]json.RawMessage) []state.Run {
				t.Helper()
				var runs []state.Run
				if err := json.Unmarshal(result["runs"], &runs); err != nil {
					t.Fatal(err)
				}
				return runs
			}
			alice := Caller{ID: "alice"}
			page := get("?workspaceId=project&planDigest=sha256:one&callerId=me&sort=created&limit=1", alice, 200)
			var counts map[string]int
			if err := json.Unmarshal(page["states"], &counts); err != nil {
				t.Fatal(err)
			}
			if counts["running"] != 1 || counts["succeeded"] != 1 || len(counts) != 2 {
				t.Fatalf("counts escaped scope: %v", counts)
			}
			runs := ids(page)
			if len(runs) != 1 || runs[0].ID != "b" || string(page["total"]) != "2" || string(page["hasMore"]) != "true" || runs[0].OriginLabel != "Editor <local>" {
				t.Fatalf("bad page: %s", page)
			}
			page = get("?workspaceId=project&planDigest=sha256:one&callerId=me&sort=created&limit=1&offset=1", alice, 200)
			if runs = ids(page); len(runs) != 1 || runs[0].ID != "a" || string(page["hasMore"]) != "false" {
				t.Fatalf("bad next page: %s", page)
			}
			for _, suffix := range []string{"", "&limit=10"} {
				runs = ids(get("?workspaceId=project&state=running"+suffix, alice, 200))
				if len(runs) != 2 {
					t.Fatalf("state filter: %+v", runs)
				}
				runs = ids(get("?workspaceId=project&createdAfter=2026-01-02T12:00:00Z&createdBefore=2026-01-02T12:00:00.000000001Z"+suffix, alice, 200))
				if len(runs) != 1 || runs[0].ID != "a" {
					t.Fatalf("date boundaries: %+v", runs)
				}
				runs = ids(get("?workspaceId=project&createdAfter=2026-01-02T13:00:00%2B01:00&createdBefore=2026-01-02T13:00:01%2B01:00"+suffix, alice, 200))
				if len(runs) != 2 {
					t.Fatalf("offset dates: %+v", runs)
				}
				if runs = ids(get("?workspaceId=missing"+suffix, alice, 200)); len(runs) != 0 {
					t.Fatal("unrelated workspace matched")
				}
			}
			if runs = ids(get("", alice, 200)); len(runs) != 5 {
				t.Fatal("generic discovery lost legacy run")
			}
			get("?callerId=bob", alice, 403)
			if runs = ids(get("?callerId=bob", Caller{ID: "admin", IsAdmin: true}, 200)); len(runs) != 1 || runs[0].ID != "c" {
				t.Fatal("admin scope")
			}
			get("?callerId=me", Caller{}, 403)
			get("?createdAfter=bad", alice, 400)
			get("?createdAfter=2026-01-03T00:00:00Z&createdBefore=2026-01-02T00:00:00Z", alice, 400)
		})
	}
}
