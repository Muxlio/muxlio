package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestGCReachabilityAndDurableExpiration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordinator.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(100 * time.Hour)
	policy := state.Retention{EventHistory: time.Hour, StdoutStderr: 2 * time.Hour, Artifacts: 3 * time.Hour, Caches: 4 * time.Hour}
	blobs := map[string]string{}
	put := func(name string) string {
		t.Helper()
		b, err := local.Put([]byte(name), "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		blobs[name] = b.Digest
		return b.Digest
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"active", "paused", "pinned", "deadline", "parent", "grandparent", "fork", "expired", "retained"} {
		digest := put(id)
		must(s.CreateRun(&state.Run{ID: id, Input: map[string]any{"file": map[string]any{"digest": digest}}}))
		if id != "active" && id != "fork" {
			must(s.SetRunState(id, "succeeded"))
		}
	}
	must(s.SetRunState("paused", "paused"))
	must(s.PinRetention("run", "pinned", true))
	deadline := now.Add(time.Hour)
	must(s.SetRunRetention("deadline", "", &deadline))
	must(s.SetRunRetention("parent", "grandparent", nil))
	// Setting a deadline later must preserve the existing parent dependency.
	must(s.SetRunRetention("parent", "", nil))
	must(s.SetRunRetention("fork", "parent", nil))
	must(s.SetRunRetention("retained", "", &deadline))
	// Retained final outputs and activation inputs protect nested references.
	output := put("output")
	must(s.SetRunOutput("retained", map[string]any{"nested": []any{map[string]any{"digest": output}}}))
	activation := put("activation")
	input, _ := json.Marshal(map[string]any{"ref": "cas://" + activation})
	// Escaped JSON strings must be decoded before discovering references.
	input = []byte(strings.ReplaceAll(string(input), "sha256:", `\u0073ha256:`))
	must(s.CreateActivation(&state.Activation{ID: "scope", RunID: "active", NodeID: "node", State: "running", Inputs: input}))
	snapshot := put("snapshot")
	must(s.AppendEvent("retained", state.EventArtifactSnapshot, "attempt", map[string]any{"snapshot": map[string]any{"digest": snapshot}}))
	orphan := put("orphan")
	pinnedArtifact := put("direct-pin")
	must(s.PinRetention("artifact", pinnedArtifact, true))
	// A pinned plan protects both artifacts and references to other plans.
	planArtifact := put("plan-artifact")
	planA := "sha256:" + strings.Repeat("a", 64)
	planB := "sha256:" + strings.Repeat("b", 64)
	must(s.SaveWorkflowVersion(planA, []byte(fmt.Sprintf(`{"child":"%s"}`, planB))))
	must(s.SaveWorkflowVersion(planB, []byte(fmt.Sprintf(`{"file":{"digest":"%s"}}`, planArtifact))))
	must(s.PinRetention("plan", planA, true))
	must(s.SaveWorkflowVersion("unused", []byte(`{}`)))
	// Reference shared with an expired run remains live through an active scope.
	must(s.CreateActivation(&state.Activation{ID: "shared", RunID: "active", NodeID: "shared", State: "running", Outputs: map[string]any{"digest": blobs["expired"]}}))
	must(s.SetActivationOutputs("shared", map[string]any{"digest": blobs["expired"]}))
	must(s.AppendLogChunk(state.LogChunk{RunID: "expired", ActivationID: "old", Stream: "stdout", Seq: 1, Data: []byte("log"), Timestamp: time.Now()}))
	before, err := s.GetEvents("expired", 0, 0)
	must(err)
	preview, err := s.CollectGarbage(local, policy, now, "")
	must(err)
	if len(preview.Artifacts) != 1 || preview.Artifacts[0].Digest != orphan || preview.Events["expired"] == 0 || preview.Logs["expired"] != 1 || len(preview.Plans) != 1 || preview.Plans[0] != "unused" {
		t.Fatalf("preview: %+v", preview)
	}
	if !local.Has(orphan) {
		t.Fatal("dry run removed bytes")
	}
	if _, err := s.CollectGarbage(local, policy, now, "wrong"); err == nil {
		t.Fatal("unreviewed cleanup accepted")
	}
	applied, err := s.CollectGarbage(local, policy, now, preview.Token)
	must(err)
	if !applied.Applied {
		t.Fatal("not applied")
	}
	for name, digest := range blobs {
		if name != "orphan" && !local.Has(digest) {
			t.Errorf("reachable %s removed", name)
		}
	}
	_, _, err = local.GetStream(context.Background(), orphan)
	if !errors.Is(err, artifacts.ErrExpired) {
		t.Fatalf("expired error: %v", err)
	}
	_, _, err = local.GetStream(context.Background(), "sha256:"+strings.Repeat("0", 64))
	if !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("missing error: %v", err)
	}
	must(s.Close())
	s, err = Open(dbPath)
	must(err)
	run, err := s.GetRun("pinned")
	must(err)
	if !run.Pinned {
		t.Fatal("pin lost on restart")
	}
	run, err = s.GetRun("deadline")
	must(err)
	if run.RetainedUntil == nil || !run.RetainedUntil.Equal(deadline) {
		t.Fatal("deadline lost")
	}
	must(s.AppendEvent("expired", "after-gc", "", nil))
	after, err := s.GetEvents("expired", 0, 0)
	must(err)
	if len(after) != 1 || after[0].Seq <= before[len(before)-1].Seq {
		t.Fatalf("event sequence reused: %+v", after)
	}
	must(s.PinRetention("plan", planA, false))
	second, err := s.CollectGarbage(local, policy, now, "")
	must(err)
	if len(second.Plans) != 2 {
		t.Fatalf("unpin failed: %+v", second)
	}
	// Invalidate a reviewed plan by making an orphan reachable before apply.
	newOrphan := put("new-orphan")
	preview, err = s.CollectGarbage(local, policy, now, "")
	must(err)
	must(s.SetRunOutput("active", map[string]any{"digest": newOrphan}))
	if _, err := s.CollectGarbage(local, policy, now, preview.Token); err == nil {
		t.Fatal("stale preview accepted")
	}
	if !local.Has(newOrphan) {
		t.Fatal("newly reachable artifact removed")
	}
}

func TestGCCategoryRetentionAndSnapshotRoots(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		events, logs, objects             time.Duration
		wantEvents, wantLogs, wantObjects bool
	}{
		{"independent", time.Hour, 200 * time.Hour, 200 * time.Hour, false, false, false},
		{"retained-history", 200 * time.Hour, time.Hour, time.Hour, false, true, false},
		{"snapshot-outlives-history", time.Hour, time.Hour, 200 * time.Hour, false, true, false},
		{"all-expired", time.Hour, time.Hour, time.Hour, true, true, true},
		{"disabled", 0, 0, 0, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			local, err := artifacts.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			b, err := local.Put([]byte("observed"), "")
			if err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.CreateRun(&state.Run{ID: "run"}))
			must(s.SetRunState("run", "failed"))
			must(s.AppendEvent("run", state.EventArtifactSnapshot, "attempt", map[string]any{"snapshot": map[string]any{"digest": b.Digest}}))
			must(s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", Stream: "stderr", Seq: 1, Data: []byte("err"), Timestamp: time.Now()}))
			policy := state.Retention{EventHistory: tc.events, StdoutStderr: tc.logs, Artifacts: tc.objects}
			preview, err := s.CollectGarbage(local, policy, time.Now().Add(100*time.Hour), "")
			must(err)
			_, err = s.CollectGarbage(local, policy, time.Now().Add(100*time.Hour), preview.Token)
			must(err)
			events, err := s.GetEvents("run", 0, 0)
			must(err)
			snapshots := 0
			for _, e := range events {
				if e.Type == state.EventArtifactSnapshot {
					snapshots++
				}
			}
			if (snapshots == 0) != tc.wantEvents {
				t.Errorf("snapshot count %d", snapshots)
			}
			logs, err := s.GetLogChunks("run", "")
			must(err)
			if (len(logs) == 0) != tc.wantLogs {
				t.Errorf("logs %d", len(logs))
			}
			if (!local.Has(b.Digest)) != tc.wantObjects {
				t.Errorf("artifact presence %v", local.Has(b.Digest))
			}
		})
	}
}

func TestGCRetentionRejectsInvalidMetadata(t *testing.T) {
	s := openTemp(t)
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.CreateRun(&state.Run{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRunRetention("a", "b", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunRetention("b", "a", nil); err == nil {
		t.Fatal("accepted ancestor cycle")
	}
	if err := s.SetRunRetention("a", "missing", nil); err == nil {
		t.Fatal("accepted missing parent")
	}
	if _, err := s.CollectGarbage(local, state.Retention{Artifacts: -1}, time.Now(), ""); err == nil {
		t.Fatal("accepted negative policy")
	}
	if _, err := s.db.Exec(`UPDATE runs SET input_json='broken' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectGarbage(local, state.DefaultRetention(), time.Now(), ""); err == nil {
		t.Fatal("corrupt roots ignored")
	}
}

func TestGCPersistedAuxiliaryRoots(t *testing.T) {
	for _, source := range []string{"diagnostic", "input-schema", "schedule-input", "pending-delivery", "requeued-delivery", "deadletter-delivery"} {
		t.Run(source, func(t *testing.T) {
			s := openTemp(t)
			local, err := artifacts.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			blob, err := local.Put([]byte(source), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateRun(&state.Run{ID: "active"}); err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"file":"cas://%s"}`, blob.Digest)
			switch source {
			case "diagnostic":
				// Diagnostics without an attempt record can arrive before dispatch.
				err = s.RecordWorkerDiagnostic("attempt", []byte(raw))
			case "input-schema":
				_, err = s.db.Exec(`INSERT INTO input_requests(run_id,request_id,name,schema_json,created_at) VALUES('active','input','input',?,?)`, raw, rfc3339(time.Now()))
			case "schedule-input":
				_, err = s.db.Exec(`INSERT INTO schedules(id,definition_json,plan_json) VALUES('schedule',?,'{}')`, raw)
			default:
				status := strings.TrimSuffix(source, "-delivery")
				_, err = s.db.Exec(`INSERT INTO subscription_deliveries(id,subscription_id,state,due,record_json) VALUES('delivery','subscription',?,0,?)`, status, raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			policy := state.Retention{Artifacts: time.Hour}
			now := time.Now().Add(100 * time.Hour)
			preview, err := s.CollectGarbage(local, policy, now, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Artifacts) != 0 {
				t.Fatalf("reachable artifact selected: %+v", preview)
			}
			if _, err := s.CollectGarbage(local, policy, now, preview.Token); err != nil {
				t.Fatal(err)
			}
			if !local.Has(blob.Digest) {
				t.Fatal("reachable content removed")
			}
		})
	}
}

func TestGCUnfinishedScopesProtectTerminalRunAndForkAncestors(t *testing.T) {
	for _, status := range []string{"running", "reconciliation", "active-assignment", "uncertain-assignment"} {
		t.Run(status, func(t *testing.T) {
			s := openTemp(t)
			local, err := artifacts.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			blob, err := local.Put([]byte(status), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"parent", "child"} {
				if err := s.CreateRun(&state.Run{ID: id, Input: map[string]any{"digest": blob.Digest}}); err != nil {
					t.Fatal(err)
				}
				if err := s.SetRunState(id, "cancelled"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SetRunRetention("child", "parent", nil); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(status, "-assignment") {
				_, err = s.db.Exec(`INSERT INTO assignments(activation_id,run_id,worker_id,generation,lease_until,state) VALUES('act','child','worker',1,?,?)`, rfc3339(time.Now()), strings.TrimSuffix(status, "-assignment"))
			} else {
				attempt := state.ExecutionAttempt{ID: "attempt", RunID: "child", ActivationID: "act", State: "running", Executor: "test"}
				if status == "reconciliation" {
					attempt.State = "failed"
					attempt.AwaitReconciliationReason = "operator resolution required"
				}
				err = s.RecordExecutionAttempt(attempt)
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := s.CollectGarbage(local, state.Retention{EventHistory: time.Hour, StdoutStderr: time.Hour, Artifacts: time.Hour}, time.Now().Add(100*time.Hour), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Artifacts) != 0 || len(report.Events) != 0 {
				t.Fatalf("unfinished scope or ancestor selected: %+v", report)
			}
		})
	}
}

func TestGCUnreadSubscriptionEventsRetainArtifacts(t *testing.T) {
	s := openTemp(t)
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	blob, err := local.Put([]byte("unread event only"), "")
	must(err)
	must(s.CreateRun(&state.Run{ID: "run"}))
	must(s.SetRunState("run", "succeeded"))
	must(s.AppendEvent("run", "custom.artifact", "", map[string]any{"digest": blob.Digest}))
	events, err := s.GetEvents("run", 0, 0)
	must(err)
	last := events[len(events)-1].Seq
	for _, id := range []string{"fast", "slow", "disabled"} {
		_, err = s.db.Exec(`INSERT INTO subscriptions(id,definition_json,secret) VALUES(?,?,'')`, id, fmt.Sprintf(`{"enabled":%t}`, id != "disabled"))
		must(err)
	}
	must(s.SetCursor("fast:run", last))
	policy := state.Retention{EventHistory: time.Hour, StdoutStderr: time.Hour, Artifacts: time.Hour}
	now := time.Now().Add(100 * time.Hour)
	preview, err := s.CollectGarbage(local, policy, now, "")
	must(err)
	if len(preview.Events) != 0 || len(preview.Artifacts) != 0 {
		t.Fatalf("unread subscription backlog selected: %+v", preview)
	}
	_, err = s.CollectGarbage(local, policy, now, preview.Token)
	must(err)
	remaining, err := s.GetEvents("run", 0, 0)
	must(err)
	if len(remaining) != len(events) || !local.Has(blob.Digest) {
		t.Fatal("subscription backlog lost")
	}
	must(s.SetCursor("slow:run", last))
	preview, err = s.CollectGarbage(local, policy, now, "")
	must(err)
	if preview.Events["run"] != len(events) || len(preview.Artifacts) != 1 {
		t.Fatalf("consumed history not collectible: %+v", preview)
	}
}

func TestGCApprovalRejectsReplacedLogRows(t *testing.T) {
	s := openTemp(t)
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateRun(&state.Run{ID: "run"}))
	must(s.SetRunState("run", "succeeded"))
	_, err = s.db.Exec(`INSERT INTO log_chunks(run_id,activation_id,stream,seq,data,timestamp) VALUES('run','act','stdout',1,'reviewed',?)`, rfc3339(time.Now()))
	must(err)
	policy := state.Retention{StdoutStderr: time.Hour}
	now := time.Now().Add(100 * time.Hour)
	preview, err := s.CollectGarbage(local, policy, now, "")
	must(err)
	_, err = s.db.Exec(`UPDATE log_chunks SET seq=2,data='unreviewed' WHERE run_id='run'`)
	must(err)
	if _, err := s.CollectGarbage(local, policy, now, preview.Token); err == nil {
		t.Fatal("stale approval deleted replacement logs")
	}
	logs, err := s.GetLogChunks("run", "")
	must(err)
	if len(logs) != 1 || logs[0].Seq != 2 {
		t.Fatal("replacement log lost")
	}
}
