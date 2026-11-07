package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestCompleteGraphAndNewestFirstHistory(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store = db
			}
			if err := store.CreateRun(&state.Run{ID: "history"}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				act := &state.Activation{ID: fmt.Sprintf("act-%03d", i), RunID: "history", NodeID: "step", ScopePath: []string{fmt.Sprintf("batch[%d]", i)}, State: "running", Inputs: json.RawMessage(`{"large":"input"}`)}
				if err := store.CreateActivation(act); err != nil {
					t.Fatal(err)
				}
			}
			// Completion order differs from creation order.
			for i := 99; i >= 0; i-- {
				if err := store.SetActivationOutputs(fmt.Sprintf("act-%03d", i), map[string]any{"result": i}); err != nil {
					t.Fatal(err)
				}
			}
			handler := NewRouter(store, nil)
			get := func(path string, target any) {
				t.Helper()
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/runs/history/"+path, nil))
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
				}
				if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
					t.Fatal(err)
				}
			}
			var graph struct {
				Activations []state.Activation
				Scopes      []any
			}
			get("graph", &graph)
			if len(graph.Activations) != 100 || len(graph.Scopes) != 100 {
				t.Fatalf("incomplete graph: %d nodes, %d scopes", len(graph.Activations), len(graph.Scopes))
			}
			for _, a := range graph.Activations {
				if a.Inputs != nil || a.Outputs != nil {
					t.Fatal("graph embeds input/output payloads")
				}
			}
			var outputs struct {
				Outputs []state.OutputEntry
				HasMore bool
			}
			get("outputs?limit=7", &outputs)
			if len(outputs.Outputs) != 7 || !outputs.HasMore {
				t.Fatal("unbounded output page")
			}
			for i, item := range outputs.Outputs {
				if item.ActivationID != fmt.Sprintf("act-%03d", i) {
					t.Fatalf("output %d is %s", i, item.ActivationID)
				}
			}
			get("outputs?limit=7&offset=7", &outputs)
			if outputs.Outputs[0].ActivationID != "act-007" {
				t.Fatal("output page did not advance")
			}
			var page struct {
				Events     []state.Event
				NextBefore int
				HasMore    bool
			}
			latest, err := store.(state.RunViewStore).LatestEventSeq("history")
			if err != nil {
				t.Fatal(err)
			}
			get("events?order=desc&limit=7", &page)
			if len(page.Events) != 7 || !page.HasMore {
				t.Fatal("unbounded event page")
			}
			for i, e := range page.Events {
				if e.Seq != latest-i {
					t.Fatal("events are not newest first")
				}
			}
			cursor := page.NextBefore
			if err := store.AppendEvent("history", "custom", "", nil); err != nil {
				t.Fatal(err)
			}
			get(fmt.Sprintf("events?order=desc&limit=7&before=%d", cursor), &page)
			if page.Events[0].Seq != latest-7 {
				t.Fatal("new event shifted a historical cursor")
			}
		})
	}
}
