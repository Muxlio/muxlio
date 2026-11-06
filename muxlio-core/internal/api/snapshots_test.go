package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestSnapshotIngestionVerifiesContentAndFencesCompletedWorkers(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(s)
	driver, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.Artifacts = driver
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", AttemptID: "attempt", ActivationID: "act", RunID: "run", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("claim: %+v %v", a, err)
	}
	ref, err := driver.PutStream(context.Background(), strings.NewReader("draft"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerLogAppend{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, Stream: "snapshot", Seq: 1,
		Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: ref.Digest, Size: ref.Size + 1}}
	if err := c.AppendWorkerLog("worker", req); err == nil {
		t.Fatal("accepted incorrect size")
	}
	req.Snapshot.Size = ref.Size
	if err := c.AppendWorkerLog("worker", req); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendWorkerLog("worker", req); err != nil {
		t.Fatal(err)
	}
	history, err := state.ListSnapshots(s, "run", "act", "attempt", false)
	if err != nil || len(history) != 1 || history[0].Payload["workerId"] != "worker" || history[0].Payload["assignmentId"] != "assignment" {
		t.Fatalf("snapshot provenance: %+v %v", history, err)
	}
	if err := c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "failed"}); err != nil {
		t.Fatal(err)
	}
	req.Seq++
	if err := c.AppendWorkerLog("worker", req); err == nil {
		t.Fatal("completed assignment published a snapshot")
	}
}

func TestSnapshotAPIHistoryCurrentAndProgressReconnect(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	for seq := 1; seq <= 2; seq++ {
		if err := s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", AttemptID: "attempt", Stream: "agent", Seq: seq, Data: []byte("progress")}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", AttemptID: "attempt", Stream: "snapshot", Seq: seq, Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: "digest"}}); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(s, nil)
	get := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := get("/v1/runs/run/snapshots?limit=1")
	if first["hasMore"] != true || len(first["snapshots"].([]any)) != 1 {
		t.Fatalf("first page: %v", first)
	}
	second := get("/v1/runs/run/snapshots?after=3&limit=1")
	if second["hasMore"] != false || second["nextAfter"] != float64(5) {
		t.Fatalf("second page: %v", second)
	}
	current := get("/v1/runs/run/snapshots?current=true&attemptId=attempt")
	if len(current["snapshots"].([]any)) != 1 || current["nextAfter"] != float64(5) {
		t.Fatalf("current: %v", current)
	}
	reconnect := get("/v1/runs/run/events?after=3")
	events := reconnect["events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["type"] != state.EventProgress {
		t.Fatalf("reconnect: %v", reconnect)
	}
	if len(get("/v1/runs/run/snapshots?attemptId=other")["snapshots"].([]any)) != 0 {
		t.Fatal("attempt filter ignored")
	}
}
