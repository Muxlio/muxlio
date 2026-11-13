package main

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestSubmittedDiscoveryAcrossClientsAndRestart(t *testing.T) {
	for _, mode := range []string{"inline", "inline-keyed", "registered", "registered-keyed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.db")
			st, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			root := t.TempDir()
			makeRunner := func() *serverRunner {
				c := api.NewCoordinator(st)
				c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "local", Path: root, Aliases: []string{"client-alias"}}, "other": {WorkerID: "local", Path: root}}
				return &serverRunner{store: st, exec: c, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
			}
			runner := makeRunner()
			request := func(method, path, body, key string) *httptest.ResponseRecorder {
				// Every request is a fresh client with the same coordinator credential.
				h := api.WithAuth(api.NewRouter(st, runner), "client", "worker")
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer client")
				if key != "" {
					req.Header.Set("Idempotency-Key", key)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				return w
			}
			raw := `{"irVersion":"1.0","name":"discovery","root":"root","subgraphs":{"root":{"nodes":[]}}}`
			selector := `"plan":` + raw
			if strings.HasPrefix(mode, "registered") {
				w := request("POST", "/v1/workflow-versions", raw, "")
				if w.Code != 201 {
					t.Fatal(w.Body.String())
				}
				var version map[string]string
				_ = json.Unmarshal(w.Body.Bytes(), &version)
				selector = `"planDigest":"` + version["digest"] + `"`
			}
			key := ""
			if strings.HasSuffix(mode, "keyed") {
				key = "invocation"
			}
			body := `{` + selector + `,"workspaceId":"client-alias","originLabel":"Editor <one>","callerId":"spoofed","input":{}}`
			w := request("POST", "/v1/runs", body, key)
			if w.Code != 201 {
				t.Fatalf("submit: %d %s", w.Code, w.Body.String())
			}
			var result map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			id := result["runId"]
			runner.wg.Wait()
			run, err := st.GetRun(id)
			if err != nil {
				t.Fatal(err)
			}
			if run.WorkspaceID != "project" || run.OriginLabel != "Editor <one>" || run.CallerID == "" || run.CallerID == "spoofed" {
				t.Fatalf("metadata: %+v", run)
			}
			if err := st.CreateRun(&state.Run{ID: "legacy"}); err != nil {
				t.Fatal(err)
			}
			check := func() {
				for _, tc := range []struct {
					query string
					count int
				}{
					{"?workspaceId=project&limit=1&callerId=me", 1},
					{"?workspaceId=client-alias&limit=1", 1},
					{"?workspaceId=project&planDigest=" + run.PlanDigest + "&limit=1", 1},
					{"?workspaceId=other&limit=1", 0},
					{"?limit=10", 2},
				} {
					w := request("GET", "/v1/runs"+tc.query, "", "")
					if w.Code != 200 {
						t.Fatalf("list: %d %s", w.Code, w.Body.String())
					}
					var page struct {
						Runs []state.Run `json:"runs"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					if len(page.Runs) != tc.count {
						t.Fatalf("%s: %s", tc.query, w.Body.String())
					}
					if tc.count == 1 && (page.Runs[0].ID != id || page.Runs[0].OriginLabel != "Editor <one>") {
						t.Fatal("discovery lost submitted metadata")
					}
				}
			}
			check()
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			runner = makeRunner()
			check()
			if key != "" {
				w := request("POST", "/v1/runs", strings.Replace(body, "Editor <one>", "Editor <two>", 1), key)
				if w.Code != 200 {
					t.Fatalf("retry: %s", w.Body.String())
				}
				var result map[string]string
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				if result["runId"] != id {
					t.Fatal(fmt.Sprint(result))
				}
				check()
			}
			// Removing execution authority must not hide retained historical runs.
			runner.exec.(*api.Coordinator).Workspaces = nil
			for _, selector := range []string{"project", "unknown"} {
				w := request("GET", "/v1/runs?workspaceId="+selector+"&limit=10", "", "")
				if w.Code != 200 {
					t.Fatalf("historical discovery: %d %s", w.Code, w.Body.String())
				}
				var page struct {
					Runs []state.Run `json:"runs"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if selector == "project" && (len(page.Runs) != 1 || page.Runs[0].ID != id) {
					t.Fatalf("retained workspace run missing: %s", w.Body.String())
				}
				if selector == "unknown" && len(page.Runs) != 0 {
					t.Fatal("unknown workspace matched unrelated runs")
				}
			}
			if w := request("POST", "/v1/runs", body, ""); w.Code != 400 {
				t.Fatalf("discovery granted execution authority: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
