package api

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunPagesKeepLargePayloadsOutOfLists(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				var err error
				s, err = sqlite.Open(filepath.Join(t.TempDir(), "pages.db"))
				if err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 105; i++ {
				id := fmt.Sprintf("run:node-%03d", i)
				if err := s.CreateActivation(&state.Activation{ID: id, RunID: "run", NodeID: fmt.Sprintf("node-%03d", i), State: "running", Outputs: map[string]any{"report": "LARGE_OUTPUT_SENTINEL"}, Inputs: json.RawMessage(`{"value":"LARGE_INPUT_SENTINEL"}`)}); err != nil {
					t.Fatal(err)
				}
				if err := s.SetActivationOutputs(id, map[string]any{"report": "LARGE_OUTPUT_SENTINEL"}); err != nil {
					t.Fatal(err)
				}
				if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id + "-attempt", RunID: "run", ActivationID: id, State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Result: &state.AttemptResult{ExecutionSucceeded: true, Outputs: map[string]any{"value": "LARGE_OUTPUT_SENTINEL"}}, Usage: &state.TokenUsage{}}); err != nil {
					t.Fatal(err)
				}
				if err := s.AppendEvent("run", state.EventArtifactSnapshot, id+"-attempt", map[string]any{"activationId": id, "snapshot": map[string]any{"name": "report", "digest": "sha256:test"}}); err != nil {
					t.Fatal(err)
				}
			}
			h := NewRouter(s, nil)
			get := func(path string) map[string]any {
				t.Helper()
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "LARGE_") {
					t.Fatalf("list loaded contents: %s", path)
				}
				var result map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			for _, entry := range []struct{ path, key string }{{"graph", "activations"}, {"attempts?details=false", "attempts"}, {"outputs", "outputs"}} {
				seen := map[string]bool{}
				for offset := 0; offset < 105; offset += 50 {
					separator := "?"
					if strings.Contains(entry.path, "?") {
						separator = "&"
					}
					page := get(fmt.Sprintf("/v1/runs/run/%s%soffset=%d&limit=50", entry.path, separator, offset))
					items := page[entry.key].([]any)
					expected := 50
					if offset == 100 {
						expected = 5
					}
					if len(items) != expected || page["hasMore"] != (offset < 100) {
						t.Fatalf("bad page: %v", page)
					}
					for _, item := range items {
						raw, _ := json.Marshal(item)
						if seen[string(raw)] {
							t.Fatal("duplicate row across pages")
						}
						seen[string(raw)] = true
					}
				}
				if len(seen) != 105 {
					t.Fatalf("lost rows: %d", len(seen))
				}
			}
			summary := get("/v1/runs/run?summary=true")["overview"].(map[string]any)
			if summary["activations"] != float64(105) || summary["attempts"] != float64(105) {
				t.Fatalf("partial totals: %v", summary)
			}
			if summary["tokens"].(map[string]any)["input"] != float64(0) {
				t.Fatal("reported zero lost")
			}
			page := get("/v1/runs/run/snapshots?limit=20")
			if len(page["snapshots"].([]any)) != 20 || page["hasMore"] != true {
				t.Fatal("snapshots unbounded")
			}
			next := get(fmt.Sprintf("/v1/runs/run/snapshots?limit=20&after=%.0f", page["nextAfter"]))
			if next["snapshots"].([]any)[0].(map[string]any)["seq"].(float64) <= page["nextAfter"].(float64) {
				t.Fatal("snapshot cursor repeated")
			}
			// Run indexes and the global artifact browser also avoid result bodies.
			for i := 0; i < 12; i++ {
				if err := s.CreateRun(&state.Run{ID: fmt.Sprintf("indexed-%02d", i), WorkflowName: "searchable", Input: map[string]any{"value": "LARGE_INPUT_SENTINEL"}}); err != nil {
					t.Fatal(err)
				}
			}
			indexed := get("/v1/runs?limit=5&offset=5&q=searchable")
			if len(indexed["runs"].([]any)) != 5 || indexed["total"] != float64(12) || indexed["hasMore"] != true {
				t.Fatalf("bad run index: %v", indexed)
			}
			ref := map[string]any{"digest": "sha256:content", "mime": "text/plain", "path": "report.txt", "data": "LARGE_OUTPUT_SENTINEL"}
			if err := s.CreateActivation(&state.Activation{ID: "run:artifact", RunID: "run", NodeID: "artifact", State: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetActivationOutputs("run:artifact", map[string]any{"report": ref}); err != nil {
				t.Fatal(err)
			}
			artifacts := get("/v1/outputs?limit=5&q=report.txt")["outputs"].([]any)
			if len(artifacts) != 1 || artifacts[0].(map[string]any)["digest"] != "sha256:content" {
				t.Fatalf("artifact index: %v", artifacts)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/activations/run:node-000", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "LARGE_OUTPUT_SENTINEL") {
				t.Fatal("activation details unavailable")
			}
			if err := s.CreateRun(&state.Run{ID: "other"}); err != nil {
				t.Fatal(err)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/other/activations/run:node-000", nil))
			if w.Code != 404 {
				t.Fatal("activation details crossed run boundary")
			}
		})
	}
}
