package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestReferencePlansValidate(t *testing.T) {
	for _, name := range []string{"sequential", "parallel-map", "mixed-provider", "large-artifact", "cancellation", "recovery", "coding", "agent-authored"} {
		t.Run(name, func(t *testing.T) {
			p, err := workload(name, 4, "agent/one", "agent/two")
			if err != nil {
				t.Fatal(err)
			}
			if errs := plan.Validate(p); plan.HasErrors(errs) {
				t.Fatalf("invalid plan: %+v", errs)
			}
			a, err := p.ComputeDigest()
			if err != nil {
				t.Fatal(err)
			}
			b, err := p.ComputeDigest()
			if err != nil || a != b {
				t.Fatal("unstable plan digest", err)
			}
		})
	}
	for _, args := range [][3]string{{"mixed-provider", "", ""}, {"mixed-provider", "agent/one", "agent/one"}, {"coding", "", ""}, {"missing", "", ""}} {
		if _, err := workload(args[0], 2, args[1], args[2]); err == nil {
			t.Fatalf("accepted invalid configuration %+v", args)
		}
	}
}

func authoredPlan() *plan.Plan {
	c, max := 2, 12
	p := base("authored", []plan.Node{{ID: "items", Kind: plan.KindMap, Items: &plan.Expr{Op: "input"}, Key: &plan.Expr{Op: "item"}, BodyRef: "item", Concurrency: &c, MaxItems: &max}}, map[string]plan.Binding{"records": ref("items", "items")})
	p.Subgraphs["item"] = plan.Subgraph{Nodes: []plan.Node{{ID: "copy", Kind: plan.KindProject, Inputs: map[string]plan.Binding{"record": {Expr: &plan.Expr{Op: "item"}}}}}, Output: map[string]plan.Binding{"record": ref("copy", "record")}}
	return p
}
func TestAgentAuthoredWorkflowVerifiedByExecution(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-output", "executor", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			p := authoredPlan()
			switch mode {
			case "wrong-output":
				sg := p.Subgraphs["item"]
				sg.Nodes[0].Inputs["record"] = literal("wrong")
				p.Subgraphs["item"] = sg
			case "executor":
				sg := p.Subgraphs["item"]
				sg.Nodes[0].Kind = plan.KindExecute
				sg.Nodes[0].Executor = "command"
				p.Subgraphs["item"] = sg
			case "cycle":
				sg := p.Subgraphs["root"]
				sg.Nodes[0].BodyRef = "root"
				p.Subgraphs["root"] = sg
			}
			run := &state.Run{Output: map[string]any{"data": string(raw(p)), "path": "workflow.json"}}
			err := verifyAuthored(run, t.TempDir())
			if (mode == "valid") != (err == nil) {
				t.Fatalf("mode %s: %v", mode, err)
			}
		})
	}
}

type blockingExecutor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return workers.ExecuteResult{}, nil
	case <-req.Context.Done():
		return workers.ExecuteResult{}, req.Context.Err()
	}
}
func TestAdmissionQueueAndCancellation(t *testing.T) {
	inner := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	x := &measuredExecutor{inner: inner, slots: make(chan struct{}, 1)}
	first := make(chan error, 1)
	go func() {
		_, err := x.Execute(workers.ExecuteRequest{Context: context.Background(), AttemptID: "first"})
		first <- err
	}()
	<-inner.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Execute(workers.ExecuteRequest{Context: ctx, AttemptID: "cancelled"}); err != context.Canceled {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() {
		_, err := x.Execute(workers.ExecuteRequest{Context: context.Background(), AttemptID: "second"})
		second <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(inner.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	samples := x.snapshot()
	if len(samples) != 3 || samples[1].AttemptID != "cancelled" || !samples[1].Cancelled || samples[2].AttemptID != "second" || samples[2].DelayMS <= 0 || samples[2].StartedAt.Before(samples[2].QueuedAt) {
		t.Fatalf("lost admission measurements: %+v", samples)
	}
}

func TestAgentMetadataRequired(t *testing.T) {
	valid := map[string]any{"models": "model IDs and settings", "versions": "CLI versions", "pricingBasis": "unavailable; subscription", "machine": "hardware and memory"}
	if err := validateMetadata(valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, "notes", map[string]any{}, map[string]any{"models": " "}} {
		if validateMetadata(value) == nil {
			t.Fatalf("accepted incomplete metadata: %#v", value)
		}
	}
}

type failedExecutor struct{}

func (failedExecutor) Execute(workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return workers.ExecuteResult{}, errors.New("intentional benchmark failure")
}

func TestFailedRunPreservesRawEvidence(t *testing.T) {
	for _, name := range []string{"sequential", "cancellation", "recovery"} {
		t.Run(name, func(t *testing.T) { testFailedRunPreservesRawEvidence(t, name) })
	}
}

func testFailedRunPreservesRawEvidence(t *testing.T, name string) {
	p, err := workload(name, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "failed")
	done := make(chan struct{})
	var obs observation
	go func() {
		obs, err = runCase(dir, name, p, failedExecutor{}, 1, 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run hung after execution failed before checkpoint")
	}
	if err == nil || obs.Correct || obs.Error == "" || obs.Run == nil || len(obs.Queue) != 1 {
		t.Fatalf("failure missing from observation: %+v, %v", obs, err)
	}
	for _, file := range []string{"observation.json", "plan.json", "input.json", "attempts.json", "activations.json", "events.json"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || !json.Valid(data) {
			t.Fatalf("missing or invalid %s: %v", file, err)
		}
	}
}

func TestCancelledAdmissionWithAvailableCapacity(t *testing.T) {
	inner := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	close(inner.release)
	x := &measuredExecutor{inner: inner, slots: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		if _, err := x.Execute(workers.ExecuteRequest{Context: ctx}); err != context.Canceled {
			t.Fatalf("cancelled admission returned %v", err)
		}
	}
	select {
	case <-inner.entered:
		t.Fatal("cancelled admission reached executor")
	default:
	}
	for _, sample := range x.snapshot() {
		if !sample.Cancelled {
			t.Fatal("cancelled admission recorded as started")
		}
	}
}

// Opt-in cross-adapter integration checks remain outside runtime packages.
func TestReferenceCommandIntegration(t *testing.T) {
	adapters := os.Getenv("MUXLIO_BENCHMARK_ADAPTERS")
	if adapters == "" {
		t.Skip("set MUXLIO_BENCHMARK_ADAPTERS to installed adapters to exercise real command processes")
	}
	allow := true
	for _, name := range []string{"sequential", "parallel-map", "large-artifact", "cancellation", "recovery"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			x := workers.NewScriptExecutor(adapters, filepath.Join(dir, "work"))
			x.Executors = map[string]workers.ExecutorConfig{"command": {Allow: &allow}}
			p, err := workload(name, 4, "", "")
			if err != nil {
				t.Fatal(err)
			}
			obs, err := runCase(dir, name, p, x, 2, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !obs.Correct || len(obs.Queue) == 0 || obs.WallTimeMS <= 0 || obs.ManualInterventions != 0 {
				t.Fatalf("invalid observation: %+v", obs)
			}
			if obs.Usage.CostUSD.Observed != nil || obs.Usage.Tokens.Observed != nil {
				t.Fatal("command did not report model usage; must remain unavailable")
			}
			var saved observation
			data, err := os.ReadFile(filepath.Join(dir, "observation.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if !saved.Correct || saved.Run == nil || saved.Run.PlanDigest == "" {
				t.Fatal("raw result lost correctness or plan identity")
			}
			for _, f := range []string{"plan.json", "input.json", "events.json", "attempts.json", "activations.json", "state.db"} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAgentCasesWithContractFixtures(t *testing.T) {
	adapters := os.Getenv("MUXLIO_BENCHMARK_ADAPTERS")
	if adapters == "" {
		t.Skip("requires command adapter")
	}
	root := t.TempDir()
	// Fixture executors stand in for agents only in tests. They do not establish
	// provider quality, performance, provenance, or real model usage.
	files := map[string]string{
		"report.json":   `{"critical":3,"warning":5,"info":4}`,
		"workflow.json": string(raw(authoredPlan())),
		"triage.cjs":    `exports.summarize=rows=>{if(!Array.isArray(rows))throw Error('array');const counts={critical:0,warning:0,info:0},seen=new Set();for(const row of rows){if(typeof row!=='string')throw Error('string');const p=row.split('|').map(s=>s.trim());if(p.length!==4||p.some(s=>!s)||!Object.hasOwn(counts,p[2])||seen.has(p[0]))throw Error('invalid');seen.add(p[0]);counts[p[2]]++}return counts}`,
	}
	for _, name := range []string{"reference/author", "reference/reviewer"} {
		script := "#!/usr/bin/env node\nconst fs=require('node:fs'),path=require('node:path');const req=JSON.parse(fs.readFileSync(0,'utf8'));const files=" + string(raw(files)) + ";for(const decl of Object.values(req.outputs)){fs.writeFileSync(path.join(req.workDir,decl.path),files[decl.path])}console.log(JSON.stringify({outputs:{}}));\n"
		filename := filepath.Join(root, "reference-"+map[string]string{"reference/author": "author", "reference/reviewer": "reviewer"}[name])
		if err := os.WriteFile(filename, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		manifest := map[string]any{"manifestVersion": "1", "contractVersion": "1", "executor": name, "executorVersion": "1", "capabilities": map[string]bool{"artifacts": true}}
		if err := writeJSON(filename+".json", manifest); err != nil {
			t.Fatal(err)
		}
	}
	// Use the real command binary through a symlink; keep all fixtures isolated.
	for _, suffix := range []string{"", ".json"} {
		if err := os.Symlink(filepath.Join(adapters, "command"+suffix), filepath.Join(root, "command"+suffix)); err != nil {
			t.Fatal(err)
		}
	}
	allow := true
	for _, name := range []string{"coding", "mixed-provider", "agent-authored"} {
		t.Run(name, func(t *testing.T) {
			p, err := workload(name, 4, "reference/author", "reference/reviewer")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), name)
			x := workers.NewScriptExecutor(root, filepath.Join(dir, "work"))
			x.Executors = map[string]workers.ExecutorConfig{"command": {Allow: &allow}, "reference/author": {Allow: &allow}, "reference/reviewer": {Allow: &allow}}
			obs, err := runCase(dir, name, p, x, 2, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !obs.Correct {
				t.Fatal("fixture failed independent verification")
			}
		})
	}
}
