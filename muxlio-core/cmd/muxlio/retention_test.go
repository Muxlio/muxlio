package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestIdleMaintenanceWaitsForRequestsAndProtectsRunningEngines(t *testing.T) {
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(root, "data", "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local, err := artifacts.NewLocal(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateRun(&state.Run{ID: "expired"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetRunState("expired", "succeeded"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs", "expired", "scratch")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(48 * time.Hour)
	var gate sync.RWMutex
	gate.RLock()
	report, err := cleanupIdleRuntimeAt(store, local, root, &gate, now)
	gate.RUnlock()
	if err != nil || report != nil {
		t.Fatal("maintenance ran during HTTP request", err)
	}
	if err = store.CreateRun(&state.Run{ID: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetRunState("active", "running"); err != nil {
		t.Fatal(err)
	}
	if report, err = cleanupIdleRuntimeAt(store, local, root, &gate, now); err != nil || report != nil {
		t.Fatal("maintenance ran during active engine", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("busy cleanup removed workspace")
	}
	if err = store.SetRunState("active", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if report, err = cleanupIdleRuntimeAt(store, local, root, &gate, now); err != nil || report == nil || !report.Applied {
		t.Fatal("idle cleanup did not apply", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired scratch remains")
	}
}
