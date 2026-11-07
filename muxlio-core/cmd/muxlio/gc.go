package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func cmdGC(args []string, dataDir string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	root := fs.String("data-dir", dataDir, "runtime directory")
	dryRun := fs.Bool("dry-run", false, "preview cleanup without deleting data (default)")
	approval := fs.String("apply", "", "apply an unchanged dry-run report token")
	defaults := state.DefaultRetention()
	events := fs.Duration("events", defaults.EventHistory, "event retention (0 disables)")
	logs := fs.Duration("logs", defaults.StdoutStderr, "stdout/stderr retention (0 disables)")
	objects := fs.Duration("artifacts", defaults.Artifacts, "artifact retention and orphan grace period (0 disables)")
	caches := fs.Duration("caches", defaults.Caches, "unreferenced plan cache retention (0 disables)")
	workspaces := fs.Duration("workspaces", defaults.Workspaces, "terminal run workspace retention (0 disables)")
	pinArtifact := fs.String("pin-artifact", "", "pin a local artifact digest")
	unpinArtifact := fs.String("unpin-artifact", "", "unpin an artifact digest")
	pinRun := fs.String("pin-run", "", "pin a run")
	unpinRun := fs.String("unpin-run", "", "unpin a run")
	pinPlan := fs.String("pin-plan", "", "pin an immutable plan digest")
	unpinPlan := fs.String("unpin-plan", "", "unpin a plan digest")
	fork := fs.String("fork-run", "", "record retention ancestry for an existing fork run")
	parent := fs.String("parent-run", "", "parent of --fork-run")
	until := fs.String("retain-until", "", "RFC3339 retention deadline for --fork-run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "gc accepts flags only")
		return 2
	}
	if *root == "" {
		*root = defaultRuntime()
	}
	// The same lock excludes both standalone and managed coordinators. Cleanup
	// runs offline so workers cannot publish new references between mark and sweep.
	lock, err := os.OpenFile(filepath.Join(*root, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "stop the coordinator before retention maintenance")
		return 1
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Do not create an empty coordinator database for a mistaken runtime path.
	if !fileExists(filepath.Join(*root, "data", "coordinator.db")) {
		fmt.Fprintln(os.Stderr, "coordinator database not found")
		return 1
	}
	db, err := sqlite.Open(filepath.Join(*root, "data", "coordinator.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	operations := 0
	for _, id := range []string{*pinRun, *unpinRun, *pinPlan, *unpinPlan, *pinArtifact, *unpinArtifact, *fork} {
		if id != "" {
			operations++
		}
	}
	if (*dryRun && (*approval != "" || operations > 0)) || operations > 1 || (operations > 0 && *approval != "") || (*parent != "" && *fork == "") || (*until != "" && *fork == "") {
		fmt.Fprintln(os.Stderr, "choose one retention operation or GC")
		return 2
	}
	for _, op := range []struct {
		kind, id string
		pinned   bool
	}{{"run", *pinRun, true}, {"run", *unpinRun, false}, {"plan", *pinPlan, true}, {"plan", *unpinPlan, false}, {"artifact", *pinArtifact, true}, {"artifact", *unpinArtifact, false}} {
		if op.id != "" {
			if op.kind == "artifact" && op.pinned {
				local, err := artifacts.NewLocal(filepath.Join(*root, "artifacts"))
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				reader, _, err := local.GetStream(context.Background(), op.id)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				if err := reader.Close(); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
			}
			if err := db.PinRetention(op.kind, op.id, op.pinned); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		}
	}
	if *fork != "" {
		var deadline *time.Time
		if *until != "" {
			t, err := time.Parse(time.RFC3339, *until)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			deadline = &t
		}
		if err := db.SetRunRetention(*fork, *parent, deadline); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	local, err := artifacts.NewLocal(filepath.Join(*root, "artifacts"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	report, err := db.CollectGarbage(local, state.Retention{EventHistory: *events, StdoutStderr: *logs, Artifacts: *objects, Caches: *caches, Workspaces: *workspaces}, time.Now(), *approval, filepath.Join(*root, "runs"))
	if report != nil {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
			fmt.Fprintln(os.Stderr, encodeErr)
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
