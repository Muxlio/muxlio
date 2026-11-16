package plan

import (
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestSignalWaitUsesDurableLatestDelivery(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "signals.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				store = db
			}
			if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			e := NewEngineWithStore(&Plan{}, nil, store)
			if payload, ok, err := e.consumeSignal("run", "approval"); err != nil || ok || payload != nil {
				t.Fatalf("undelivered signal: %v %v %v", payload, ok, err)
			}
			for _, value := range []string{"first", "latest"} {
				if err := store.SendSignal("run", "approval", map[string]any{"value": value}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				e = NewEngineWithStore(&Plan{}, nil, store)
				outputs, err := e.executeWait("run", &Node{ID: "wait", WaitFor: "signal", SignalName: "approval"}, nil, &EvalContext{})
				if err != nil {
					t.Fatal(err)
				}
				payload, ok := outputs["payload"].(map[string]any)
				if !ok || payload["value"] != "latest" {
					t.Fatalf("latest signal lost: %+v", outputs)
				}
			}
		})
	}
}
