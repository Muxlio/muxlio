package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestNamedWorkflowPreservesRunRetryAndSchedulePins(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := workers.NewFake().FailOnNode("first", errors.New("temporary failure"), 1)
	runner := &serverRunner{store: s, exec: exec, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	defer runner.wg.Wait()
	handler := api.WithAuth(api.NewRouter(s, runner), "admin", "worker")
	call := func(method, path, body string, want int, result any) {
		t.Helper()
		req := httptest.NewRequest(method, "/v1/"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer admin")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		if result != nil {
			if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
				t.Fatal(err)
			}
		}
	}
	submit := func(digest string) string {
		t.Helper()
		var result struct{ RunID string }
		call(http.MethodPost, "runs", fmt.Sprintf(`{"planDigest":%q,"input":{}}`, digest), 201, &result)
		runner.wg.Wait()
		return result.RunID
	}
	checkRun := func(id, digest, wantState string, pinned []byte) {
		t.Helper()
		run, err := s.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if run.PlanDigest != digest || !bytes.Equal(run.PlanJSON, pinned) || run.State != wantState {
			t.Fatalf("run %s = %+v, want digest %s, exact pin, state %s", id, run, digest, wantState)
		}
	}

	const first = `{"irVersion":"1.0","name":"Original","root":"root","subgraphs":{"root":{"nodes":[{"id":"first","kind":"execute","executor":"test/task"}]}}}`
	second := strings.ReplaceAll(strings.ReplaceAll(first, "Original", "Renamed"), "first", "second")
	var workflow state.NamedWorkflow
	call(http.MethodPost, "workflows", fmt.Sprintf(`{"workflowId":"sample","plan":%s}`, first), 201, &workflow)
	firstDigest := workflow.CurrentDigest
	firstBytes, err := s.GetWorkflowVersion(firstDigest)
	if err != nil {
		t.Fatal(err)
	}
	earlier := submit(firstDigest)
	checkRun(earlier, firstDigest, plan.RunFailed, firstBytes)

	engine := plan.NewEngineWithStore(nil, exec, s)
	queued, _, err := engine.CreateRegisteredRun(firstBytes, json.RawMessage(`{}`), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	checkRun(queued, firstDigest, plan.RunCreated, firstBytes)

	due := time.Now().UTC().Add(time.Hour)
	definition, err := json.Marshal(schedule.Definition{
		ID: "clock", WorkflowDigest: firstDigest, Input: json.RawMessage(`{}`),
		Kind: "once", StartAt: due, Overlap: "allow", MaxCatchUp: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "schedules", string(definition), 201, nil)

	call(http.MethodPost, "workflows/sample", fmt.Sprintf(`{"expectedCurrentRevision":1,"plan":%s}`, second), 200, nil)
	call(http.MethodGet, "workflows/sample", "", 200, &workflow)
	secondDigest := workflow.CurrentDigest
	if secondDigest == firstDigest || workflow.CurrentRevision != 2 {
		t.Fatalf("current version: %+v", workflow)
	}
	secondBytes, err := s.GetWorkflowVersion(secondDigest)
	if err != nil {
		t.Fatal(err)
	}
	newRun := submit(secondDigest)
	checkRun(newRun, secondDigest, plan.RunSucceeded, secondBytes)
	oldRun := submit(firstDigest)
	checkRun(oldRun, firstDigest, plan.RunSucceeded, firstBytes)

	call(http.MethodPost, "runs/"+queued+"/start", "{}", 202, nil)
	runner.wg.Wait()
	checkRun(queued, firstDigest, plan.RunSucceeded, firstBytes)
	call(http.MethodPost, "runs/"+earlier+"/retry", "{}", 200, nil)
	runner.wg.Wait()
	checkRun(earlier, firstDigest, plan.RunSucceeded, firstBytes)

	fired, err := s.TickSchedules(due)
	if err != nil || len(fired) != 1 {
		t.Fatalf("schedule fire: %v, %v", fired, err)
	}
	if err := runner.StartPersistedRun(fired[0]); err != nil {
		t.Fatal(err)
	}
	runner.wg.Wait()
	checkRun(fired[0], firstDigest, plan.RunSucceeded, firstBytes)

	calls := exec.Calls()
	wantNodes := []string{"first", "second", "first", "first", "first", "first"}
	if len(calls) != len(wantNodes) {
		t.Fatalf("executed %d nodes, want %d", len(calls), len(wantNodes))
	}
	for i, want := range wantNodes {
		if calls[i].NodeID != want {
			t.Fatalf("execution %d used node %q, want %q", i, calls[i].NodeID, want)
		}
	}
}
