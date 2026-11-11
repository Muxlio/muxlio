package integration_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Replay provider protocol events through the real adapter and worker, persist
// the interpreter's attempt to SQLite, then read it through the attempts API.
// No provider credentials or live model requests are used.
func TestAgentUsageAdaptersToAttemptsAPI(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	adapters := t.TempDir()
	build := exec.Command("go", "build", "-o", adapters+string(os.PathSeparator),
		"./cmd/agent-antigravity", "./cmd/agent-claude", "./cmd/agent-codex", "./cmd/agent-cursor", "./cmd/agent-kiro")
	build.Dir = filepath.Join(root, "muxlio-adapters")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build adapters: %v\n%s", err, out)
	}
	kiroEvents, err := os.ReadFile(filepath.Join(root, "muxlio-adapters", "cmd", "agent-kiro", "testdata", "credit-only.stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	reported := &[3]string{"120", "30", "40"}
	cursorReported := &[3]string{"160", "30", "40"}
	kiroCredit := "0.18065595854063016"
	for _, tc := range []struct {
		executor, cli, events string
		wantUsage             *[3]string
		wantCredit            *string
		wantEstimatedCostUSD  *string
	}{
		{"agent/antigravity", "agy", `{"type":"result","status":"success","stats":{"total_tokens":150,"input_tokens":120,"output_tokens":30,"cached":40,"input":80,"duration_ms":200,"tool_calls":0}}
		{"type":"result","status":"success"}`, reported, nil, nil},
		{"agent/claude", "claude", `{"type":"result","usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":40,"cache_creation_input_tokens":0},"total_cost_usd":0.0812}
{"type":"result","usage":null}`, &[3]string{"160", "30", "40"}, nil, amountPtr("0.0812")},
		{"agent/codex", "codex", `{"type":"turn.completed","usage":{"input_tokens":120,"output_tokens":30,"cached_input_tokens":40}}
		{"type":"turn.completed","usage":null}`, reported, nil, nil},
		{"agent/cursor", "agent", `{"type":"result","usage":{"inputTokens":120,"outputTokens":30,"cacheReadTokens":40}}
		{"type":"result"}`, cursorReported, nil, nil},
		{"agent/kiro", "kiro-cli", string(kiroEvents), nil, &kiroCredit, nil},
	} {
		for _, fail := range []bool{false, true} {
			name := tc.executor + "/success"
			if fail {
				name = tc.executor + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				binDir, workspace := t.TempDir(), t.TempDir()
				script := "#!/bin/sh\n/bin/cat <<'MUXLIO_EVENTS'\n" + tc.events + "\nMUXLIO_EVENTS\n"
				if fail {
					script += "exit 1\n"
				}
				if err := os.WriteFile(filepath.Join(binDir, tc.cli), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				executor := workers.NewScriptExecutor(adapters, filepath.Join(t.TempDir(), "work"))
				executor.Workspace = workspace
				executor.AgentHome = t.TempDir() // Resolve only the fixture CLI on PATH.
				p := &plan.Plan{IRVersion: "1.0", Name: "agent-usage", Root: "root", Subgraphs: map[string]plan.Subgraph{
					"root": {Nodes: []plan.Node{{ID: "agent", Kind: plan.KindExecute, Executor: tc.executor}}},
				}}
				dbPath := filepath.Join(t.TempDir(), "test.db")
				store, err := sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				engine := plan.NewEngineWithStore(p, executor, store)
				id, err := engine.CreateRun(json.RawMessage(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Run(id); (err != nil) != fail {
					t.Fatalf("run error = %v, want failure = %v", err, fail)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := sqlite.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				response := httptest.NewRecorder()
				api.NewRouter(reopened, nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/runs/"+id+"/attempts", nil))
				var body struct {
					Attempts []state.ExecutionAttempt `json:"attempts"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || len(body.Attempts) != 1 {
					t.Fatalf("attempts API: status=%d body=%s error=%v", response.Code, response.Body.String(), err)
				}
				attempt := body.Attempts[0]
				wantState := "succeeded"
				if fail {
					wantState = "failed"
				}
				if attempt.State != wantState || attempt.Accounting == nil || !attempt.Accounting.Final {
					t.Fatalf("stored attempt missing final accounting: %+v", attempt)
				}
				var tokens *[3]string
				var credit, cost *string
				for _, entry := range attempt.Accounting.Entries {
					if v := entry.Tokens; v != nil {
						if v.Input.Collected != nil && v.Output.Collected != nil && v.CacheRead.Collected != nil {
							tokens = &[3]string{string(v.Input.Collected.Value), string(v.Output.Collected.Value), string(v.CacheRead.Collected.Value)}
						}
					}
					for _, v := range entry.Credits {
						if v.Provider == "kiro" && v.Unit == "credit" && v.Amount.Collected != nil {
							value := string(v.Amount.Collected.Value)
							credit = &value
						}
					}
					for _, v := range entry.Costs {
						if v.Currency == "USD" && v.Basis == "api-equivalent" && v.Amount.Collected != nil && v.Amount.Collected.Fidelity == "provider-estimate" {
							value := string(v.Amount.Collected.Value)
							cost = &value
						}
					}
				}
				if (tokens == nil) != (tc.wantUsage == nil) || tokens != nil && *tokens != *tc.wantUsage {
					t.Fatalf("tokens=%v want=%v", tokens, tc.wantUsage)
				}
				if (credit == nil) != (tc.wantCredit == nil) || credit != nil && *credit != *tc.wantCredit {
					t.Fatalf("credits=%v want=%v", credit, tc.wantCredit)
				}
				if (cost == nil) != (tc.wantEstimatedCostUSD == nil) || cost != nil && *cost != *tc.wantEstimatedCostUSD {
					t.Fatalf("cost=%v want=%v", cost, tc.wantEstimatedCostUSD)
				}
			})
		}
	}
}

func amountPtr(v string) *string { return &v }
