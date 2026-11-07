package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestResumeDormantForkStartsEngine(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definition := &plan.Plan{IRVersion: "1.0", Name: "fork", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{}}}}
	engine := plan.NewEngineWithStore(definition, workers.NewFake(), store)
	source, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(source); err != nil {
		t.Fatal(err)
	}
	fork, err := engine.ForkRun(source, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &serverRunner{store: store, exec: workers.NewFake(), ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	if err := runner.ControlRun(fork, "pause"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ControlRun(fork, "resume"); err != nil {
		t.Fatal(err)
	}
	runner.wg.Wait()
	run, err := store.GetRun(fork)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != plan.RunSucceeded {
		t.Fatalf("resumed fork ended in %s", run.State)
	}
}
