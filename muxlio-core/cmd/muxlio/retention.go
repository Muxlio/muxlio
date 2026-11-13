package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func withMaintenanceGate(next http.Handler, gate *sync.RWMutex) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gate.RLock(); defer gate.RUnlock(); next.ServeHTTP(w, r) })
}

// Runtime cleanup is allowed only with no active HTTP request, engine, or
// unfinished worker assignment. Each apply rechecks idleness and reachability.
func cleanupIdleRuntime(store *sqlite.Store, driver artifacts.Driver, dataDir string, gate *sync.RWMutex) (*sqlite.GCReport, error) {
	return cleanupIdleRuntimeAt(store, driver, dataDir, gate, time.Now())
}
func cleanupIdleRuntimeAt(store *sqlite.Store, driver artifacts.Driver, dataDir string, gate *sync.RWMutex, now time.Time) (*sqlite.GCReport, error) {
	local, ok := driver.(*artifacts.Local)
	if !ok || !gate.TryLock() {
		return nil, nil
	}
	defer gate.Unlock()
	root := filepath.Join(dataDir, "runs")
	policy := state.DefaultRetention()
	report, err := store.CollectIdleGarbage(local, policy, now, "", root)
	if err != nil || report == nil {
		return report, err
	}
	if len(report.Artifacts)+len(report.Workspaces)+len(report.Events)+len(report.Logs)+len(report.Plans)+len(report.IdempotencyKeys) == 0 {
		return report, nil
	}
	return store.CollectIdleGarbage(local, policy, now, report.Token, root)
}
func runRetentionMaintenance(ctx context.Context, store *sqlite.Store, driver artifacts.Driver, dataDir string, gate *sync.RWMutex) {
	cleanup := func() {
		report, err := cleanupIdleRuntime(store, driver, dataDir, gate)
		if err != nil {
			fmt.Fprintln(os.Stderr, "retention cleanup deferred:", err)
			return
		}
		if report != nil && report.Applied {
			var bytes int64
			for _, w := range report.Workspaces {
				bytes += w.Bytes
			}
			for _, a := range report.Artifacts {
				bytes += a.Size
			}
			fmt.Fprintf(os.Stderr, "retention cleanup: %d workspaces, %d artifacts, %d bytes removed\n", len(report.Workspaces), len(report.Artifacts), bytes)
		}
	}
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
