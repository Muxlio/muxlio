package recovery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// TestTimerRecoveryAcrossCoordinatorRestart tests that pending timers for
// interrupted runs are preserved across store reopen and can be due/fired.
func TestTimerRecoveryAcrossCoordinatorRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "coordinator.db")

	store1, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	timerPlan := &plan.Plan{
		Name:      "timer-workflow",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:           "wait_1",
						Kind:         plan.KindWait,
						WaitFor:      "timer",
						TimerSeconds: floatPtr(60), // long timer
					},
				},
			},
		},
	}
	if _, err := timerPlan.ComputeDigest(); err != nil {
		t.Fatal(err)
	}

	exec := workers.NewFake()
	eng1 := plan.NewEngineWithStore(timerPlan, exec, store1)
	runID, err := eng1.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	// Schedule a timer simulating an active wait node
	dueAt := time.Now().Add(-1 * time.Second) // Due in the past (already elapsed)
	timerID := runID + "/wait_1:timer"
	if err := store1.ScheduleTimer(timerID, runID, runID+"/wait_1", dueAt, nil); err != nil {
		t.Fatal(err)
	}

	// Close store1 simulating coordinator crash / reboot
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// Coordinator restarts and reopens store
	store2, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	// Verify that timers survive and can be polled/fired
	dueTimers, err := store2.DueTimers(time.Now())
	if err != nil {
		t.Fatalf("polling due timers failed after restart: %v", err)
	}
	if len(dueTimers) != 1 {
		t.Fatalf("expected 1 due timer after restart, got %d", len(dueTimers))
	}
	if dueTimers[0].ID != timerID {
		t.Errorf("expected timer ID %s, got %s", timerID, dueTimers[0].ID)
	}

	// Verify run recovery reconstructed the engine
	recovered, err := plan.RecoverRuns(store2, exec)
	if err != nil {
		t.Fatalf("RecoverRuns failed: %v", err)
	}
	if _, ok := recovered[runID]; !ok {
		t.Errorf("expected run %s to be recovered, recovered: %v", runID, recovered)
	}
}

func floatPtr(f float64) *float64 {
	return &f
}
