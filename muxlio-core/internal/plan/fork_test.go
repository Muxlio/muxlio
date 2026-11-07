package plan

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func forkTestPlan(t *testing.T, effect string) *Plan {
	t.Helper()
	var p Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"fork-test","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"test","retry":{"effect":"`+effect+`"},"outputs":{"result":{}}}],"output":{"result":{"ref":{"node":"work","port":"result"}}}}}}`), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestForkSafetyAndReuse(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, effect := range []string{"pure", "manual", "idempotent-with-key", ""} {
			t.Run(backend+"/"+effect, func(t *testing.T) {
				var s state.Store = state.New()
				if backend == "sqlite" {
					var err error
					s, err = sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
					if err != nil {
						t.Fatal(err)
					}
				}
				defer s.Close()
				originalExec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"result": "original"}})
				original := NewEngineWithStore(forkTestPlan(t, effect), originalExec, s)
				id, err := original.CreateRun(json.RawMessage(`{"n":1}`))
				if err != nil {
					t.Fatal(err)
				}
				if err := original.Run(id); err != nil {
					t.Fatal(err)
				}
				before, err := s.GetRun(id)
				if err != nil {
					t.Fatal(err)
				}
				beforeEvents, err := s.GetEvents(id, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				forkExec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"result": "new"}})
				fork := NewEngineWithStore(nil, forkExec, s)
				forkID, err := fork.ForkRun(id, nil, []string{id + ":work"})
				if err != nil {
					t.Fatal(err)
				}
				forkRecord, err := s.GetRun(forkID)
				if err != nil {
					t.Fatal(err)
				}
				if len(forkExec.Calls()) != 0 || forkRecord.State != RunCreated || forkRecord.Fork == nil || forkRecord.Fork.SourceRunID != id || forkRecord.Fork.SourcePlanDigest != before.PlanDigest {
					t.Fatalf("fork creation: %+v", forkRecord)
				}
				err = fork.Run(forkID)
				if effect == "pure" {
					if err != nil {
						t.Fatal(err)
					}
					events, err := s.GetEvents(forkID, 0, 0)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, event := range events {
						if event.Type == "activation.output_reused" {
							found = event.Payload["sourceRunId"] == id && event.Payload["sourceActivationId"] == id+":work" && strings.HasPrefix(event.Payload["fingerprint"].(string), "sha256:")
						}
					}
					if !found {
						t.Fatalf("missing reuse provenance: %+v", events)
					}
					result, err := s.GetRun(forkID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(result.Output, before.Output) {
						t.Fatalf("outputs: %+v", result)
					}
				} else if err == nil || !strings.Contains(err.Error(), "fork blocked") {
					t.Fatalf("unsafe fork: %v", err)
				}
				if len(forkExec.Calls()) != 0 {
					t.Fatal("fork implicitly executed an unsafe or reused step")
				}
				after, err := s.GetRun(id)
				if err != nil {
					t.Fatal(err)
				}
				afterEvents, err := s.GetEvents(id, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) {
					t.Fatal("fork mutated source history")
				}
			})
		}
	}
}

func TestForkFingerprintInvalidation(t *testing.T) {
	for _, change := range []string{"input", "plan", "not-selected", "legacy", "mutable-file"} {
		t.Run(change, func(t *testing.T) {
			s := state.New()
			p := forkTestPlan(t, "pure")
			if change == "mutable-file" {
				sg := p.Subgraphs["root"]
				sg.Nodes[0].Outputs = map[string]OutputDecl{"result": {Path: "result.txt"}}
				p.Subgraphs["root"] = sg
			}
			exec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"result": "old"}})
			e := NewEngineWithStore(p, exec, s)
			id, err := e.CreateRun(json.RawMessage(`{"n":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Run(id); err != nil {
				t.Fatal(err)
			}
			reuse := []string{id + ":work"}
			var input json.RawMessage
			var target *Plan
			switch change {
			case "input":
				input = json.RawMessage(`{"n":2}`)
			case "plan":
				target = forkTestPlan(t, "pure")
				target.Subgraphs["root"].Nodes[0].Task = "changed"
			case "not-selected":
				reuse = nil
			case "legacy":
				// A successful checkpoint without a recorded fingerprint must be recomputed.
				legacy := NewEngineWithStore(forkTestPlan(t, "pure"), exec, s)
				id, err = legacy.CreateRun(json.RawMessage(`{"n":1}`))
				if err != nil {
					t.Fatal(err)
				}
				if err := s.CreateActivation(&state.Activation{ID: id + ":work", RunID: id, NodeID: "work", State: ActQueued}); err != nil {
					t.Fatal(err)
				}
				if err := s.SetActivationOutputs(id+":work", map[string]any{"result": "old"}); err != nil {
					t.Fatal(err)
				}
				if err := s.SetRunState(id, RunSucceeded); err != nil {
					t.Fatal(err)
				}
				reuse = []string{id + ":work"}
			}
			nextExec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"result": "new"}})
			next := NewEngineWithStore(target, nextExec, s)
			forkID, err := next.ForkRun(id, input, reuse)
			if err != nil {
				t.Fatal(err)
			}
			if err := next.Run(forkID); err != nil {
				t.Fatal(err)
			}
			if len(nextExec.Calls()) != 1 {
				t.Fatal("stale, unselected, or mutable output reused")
			}
		})
	}
}

func TestForkBlocksStatefulPureSteps(t *testing.T) {
	for _, kind := range []string{"session", "remote", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			p := forkTestPlan(t, "pure")
			node := &p.Subgraphs["root"].Nodes[0]
			switch kind {
			case "session":
				node.Session = &contract.SessionRequest{Mode: "create"}
			case "remote":
				node.RemoteTask = &contract.RemoteTaskReference{ID: "task"}
			case "workspace":
				node.WorkspaceMode = "shared"
			}
			if reusableNode(node) {
				t.Fatal("stateful pure node eligible for reuse or execution in fork")
			}
		})
	}
}

func TestForkRejectsLiveSourceAndForeignCandidate(t *testing.T) {
	s := state.New()
	e := NewEngineWithStore(forkTestPlan(t, "pure"), nil, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ForkRun(id, nil, nil); err == nil {
		t.Fatal("live source accepted")
	}
	if err := s.SetRunState(id, RunSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "foreign", RunID: "other", State: ActSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ForkRun(id, nil, []string{"foreign"}); err == nil {
		t.Fatal("foreign reuse candidate accepted")
	}
}

func TestForkDependencyOutputInvalidatesReuse(t *testing.T) {
	s := state.New()
	var p Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"dependencies","root":"root","subgraphs":{"root":{"nodes":[{"id":"first","kind":"execute","executor":"test","retry":{"effect":"pure"}},{"id":"second","kind":"execute","executor":"test","retry":{"effect":"pure"},"inputs":{"dependency":{"ref":{"node":"first","port":"result"}}}}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	originalExec := workers.NewFake().ByNode("first", workers.ExecuteResult{Outputs: map[string]any{"result": "old-dependency"}}).ByNode("second", workers.ExecuteResult{Outputs: map[string]any{"result": "old"}})
	e := NewEngineWithStore(&p, originalExec, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(id); err != nil {
		t.Fatal(err)
	}
	forkExec := workers.NewFake().ByNode("first", workers.ExecuteResult{Outputs: map[string]any{"result": "changed-dependency"}}).ByNode("second", workers.ExecuteResult{Outputs: map[string]any{"result": "new"}})
	fork := NewEngineWithStore(nil, forkExec, s)
	forkID, err := fork.ForkRun(id, nil, []string{id + ":second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fork.Run(forkID); err != nil {
		t.Fatal(err)
	}
	if len(forkExec.Calls()) != 2 {
		t.Fatal("output reused despite changed dependency")
	}
	second, err := s.GetActivation(forkID + ":second")
	if err != nil {
		t.Fatal(err)
	}
	if second.Outputs["result"] != "new" {
		t.Fatalf("stale result: %+v", second.Outputs)
	}
}

func TestForkReusesContentAddressedFile(t *testing.T) {
	s := state.New()
	p := forkTestPlan(t, "pure")
	p.Subgraphs["root"].Nodes[0].Outputs["result"] = OutputDecl{Path: "result.txt"}
	ref := map[string]any{"digest": "sha256:" + strings.Repeat("a", 64), "mime": "text/plain"}
	exec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"result": ref}})
	e := NewEngineWithStore(p, exec, s)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(id); err != nil {
		t.Fatal(err)
	}
	forkExec := workers.NewFake()
	fork := NewEngineWithStore(nil, forkExec, s)
	forkID, err := fork.ForkRun(id, nil, []string{id + ":work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fork.Run(forkID); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetActivation(forkID + ":work")
	if err != nil {
		t.Fatal(err)
	}
	if len(forkExec.Calls()) != 0 || !reflect.DeepEqual(a.Outputs["result"], ref) {
		t.Fatalf("file not reused: %+v", a)
	}
}

// Recovery must preserve the same explicit-start boundary as the HTTP API.
func TestForkRecoveryRequiresExplicitStart(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			path := filepath.Join(t.TempDir(), "state.db")
			if backend == "sqlite" {
				var err error
				s, err = sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			e := NewEngineWithStore(forkTestPlan(t, "pure"), workers.NewFake(), s)
			source, err := e.CreateRun(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Run(source); err != nil {
				t.Fatal(err)
			}
			dormant, err := e.ForkRun(source, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			active, err := e.ForkRun(source, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetRunState(active, RunRunning); err != nil {
				t.Fatal(err)
			}
			if backend == "sqlite" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			before, err := s.GetRun(dormant)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := s.GetEvents(dormant, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			exec := workers.NewFake()
			recovered, err := RecoverRuns(s, exec)
			if err != nil {
				t.Fatal(err)
			}
			if recovered[dormant] != nil {
				t.Fatal("restart implicitly starts a dormant fork")
			}
			if recovered[active] == nil {
				t.Fatal("started fork was not recovered")
			}
			if err := recovered[active].Run(active); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetRun(dormant)
			if err != nil {
				t.Fatal(err)
			}
			afterEvents, err := s.GetEvents(dormant, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			acts, err := s.ListActivations(dormant)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) || len(acts) != 0 {
				t.Fatal("recovery modified a dormant fork")
			}
			if len(exec.Calls()) != 1 {
				t.Fatal("recovery executed unexpected work")
			}
		})
	}
}
