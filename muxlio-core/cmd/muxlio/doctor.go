package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

func cmdDoctor(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL (default: http://127.0.0.1:7600)")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}

	root := *dataDir
	if root == "" {
		root = defaultRuntime()
	}
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}

	checks := make([]doctorCheck, 0)
	allPassed := true
	capacityCheck := serviceWorkerCapacityCheck(authConfigPath(root))
	checks = append(checks, capacityCheck)
	if !capacityCheck.Passed {
		allPassed = false
	}

	// 1. Check runtime dir
	info, err := os.Stat(root)
	if err == nil && info.IsDir() {
		perm := info.Mode().Perm()
		if perm&0o077 != 0 {
			checks = append(checks, doctorCheck{
				Name:    "Runtime directory permissions",
				Passed:  true,
				Message: fmt.Sprintf("%s permissions are %04o (recommended 0700)", root, perm),
				Fix:     fmt.Sprintf("chmod 700 %s", root),
			})
		} else {
			checks = append(checks, doctorCheck{
				Name:    "Runtime directory permissions",
				Passed:  true,
				Message: fmt.Sprintf("%s permissions are secure (%04o)", root, perm),
			})
		}
	} else {
		allPassed = false
		checks = append(checks, doctorCheck{
			Name:    "Runtime directory",
			Passed:  false,
			Message: fmt.Sprintf("%s does not exist", root),
			Fix:     "Run 'muxlio init' or 'bash scripts/setup.sh'",
		})
	}

	// 2. Check SQLite Database & Outbox Table
	dbPath := filepath.Join(root, "data", "coordinator.db")
	if fileExists(dbPath) {
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(2000)")
		if err == nil {
			defer db.Close()
			var integrity string
			err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity)
			if err == nil && integrity == "ok" {
				var tableCount int
				_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount)
				var outboxCount int
				_ = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE published_at IS NULL").Scan(&outboxCount)
				checks = append(checks, doctorCheck{
					Name:    "SQLite Database",
					Passed:  true,
					Message: fmt.Sprintf("Integrity ok, %d tables found, %d pending outbox messages", tableCount, outboxCount),
				})
			} else {
				checks = append(checks, doctorCheck{
					Name:    "SQLite Database",
					Passed:  false,
					Message: fmt.Sprintf("Integrity check failed: %v", err),
					Fix:     "Check coordinator.db permissions or restore backup",
				})
				allPassed = false
			}
		} else {
			checks = append(checks, doctorCheck{
				Name:    "SQLite Database",
				Passed:  false,
				Message: fmt.Sprintf("Unable to open database: %v", err),
			})
			allPassed = false
		}
	} else {
		checks = append(checks, doctorCheck{
			Name:    "SQLite Database",
			Passed:  true,
			Message: "No database yet (will be initialized on first server start)",
		})
	}

	// 3. Check adapters
	adapterDir := filepath.Join(root, "libexec", "muxlio", "adapters")
	expectedAdapters := bundledAdapterBasenames()
	foundAdapters := 0
	for _, a := range expectedAdapters {
		if fileExists(filepath.Join(adapterDir, a)) {
			foundAdapters++
		}
	}
	if foundAdapters == len(expectedAdapters) {
		checks = append(checks, doctorCheck{
			Name:    "Executor adapters",
			Passed:  true,
			Message: fmt.Sprintf("All %d bundled adapters found in %s", foundAdapters, adapterDir),
		})
	} else if foundAdapters > 0 {
		checks = append(checks, doctorCheck{
			Name:    "Executor adapters",
			Passed:  true,
			Message: fmt.Sprintf("%d of %d adapters found in %s", foundAdapters, len(expectedAdapters), adapterDir),
			Fix:     "Run 'bash scripts/setup.sh' to compile all adapters",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:    "Executor adapters",
			Passed:  false,
			Message: fmt.Sprintf("No adapters found in %s", adapterDir),
			Fix:     "Run 'bash scripts/setup.sh' to build release adapters",
		})
		allPassed = false
	}

	// Executable readiness is separate from installation.
	if caps, err := workerReadiness(root, ""); err == nil {
		for _, cap := range caps {
			for _, c := range cap.Readiness.Checks {
				checks = append(checks, doctorCheck{Name: cap.Name + " / " + c.Name, Passed: c.Passed, Message: c.Message})
			}
			if !cap.Readiness.Ready {
				allPassed = false
			}
		}
	} else {
		checks = append(checks, doctorCheck{Name: "Executor readiness", Passed: false, Message: "unable to load worker readiness configuration"})
		allPassed = false
	}

	// 4. Check UI assets
	uiIndex := filepath.Join(root, "share", "muxlio", "ui", "index.html")
	if fileExists(uiIndex) {
		checks = append(checks, doctorCheck{
			Name:    "Observability UI",
			Passed:  true,
			Message: fmt.Sprintf("UI assets found at %s", filepath.Dir(uiIndex)),
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:    "Observability UI",
			Passed:  false,
			Message: fmt.Sprintf("UI index.html not found in %s", filepath.Dir(uiIndex)),
			Fix:     "Run 'bash scripts/setup.sh' to copy UI assets",
		})
	}

	// 5. Check Node & tsx
	nodePath, nodeErr := exec.LookPath("node")
	tsxPath, _ := exec.LookPath("npx")
	if nodeErr == nil {
		checks = append(checks, doctorCheck{
			Name:    "Node.js Runtime",
			Passed:  true,
			Message: fmt.Sprintf("Node.js found at %s (npx: %s)", nodePath, tsxPath),
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:    "Node.js Runtime",
			Passed:  false,
			Message: "node executable not found in PATH",
			Fix:     "Install Node.js 18+ to enable TypeScript workflow compilation",
		})
		allPassed = false
	}

	// 6. Check Coordinator Server (if running or reachable)
	targetURL := *server
	if targetURL == "" {
		targetURL = "http://127.0.0.1:7600"
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(strings.TrimRight(targetURL, "/") + "/healthz")
	if err == nil {
		resp.Body.Close()
		checks = append(checks, doctorCheck{
			Name:    "Coordinator Service",
			Passed:  true,
			Message: fmt.Sprintf("Coordinator is healthy and responding at %s", targetURL),
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:    "Coordinator Service",
			Passed:  true,
			Message: fmt.Sprintf("Coordinator not currently running at %s (run 'muxlio server start')", targetURL),
		})
	}

	if *jsonOut {
		out, _ := json.MarshalIndent(map[string]any{
			"ok":     allPassed,
			"checks": checks,
		}, "", "  ")
		fmt.Println(string(out))
		if allPassed {
			return 0
		}
		return 1
	}

	fmt.Println("Muxlio Doctor Diagnostics:")
	for _, c := range checks {
		mark := "✓"
		if !c.Passed {
			mark = "✗"
		}
		fmt.Printf("  %s %-32s : %s\n", mark, c.Name, c.Message)
		if c.Fix != "" {
			fmt.Printf("      Fix: %s\n", c.Fix)
		}
	}

	if allPassed {
		fmt.Println("\n✓ Everything is in order!")
		return 0
	}
	fmt.Println("\n✗ Some checks require attention (see fixes above).")
	return 1
}

func serviceWorkerCapacityCheck(path string) doctorCheck {
	c, err := loadServiceConfig(path)
	// Doctor supplies the runtime's default path explicitly. Its absence uses
	// service defaults, while a missing MUXLIO_CONFIG remains a configuration error.
	if os.IsNotExist(err) && os.Getenv("MUXLIO_CONFIG") == "" {
		err = nil
	}
	if err != nil {
		return doctorCheck{Name: "Service worker capacity", Message: fmt.Sprintf("Unable to load service configuration: %v", err), Fix: "Set workerCapacity to a nonnegative integer (default 1; 0 drains), then restart the service"}
	}
	message := fmt.Sprintf("workerCapacity=%d concurrent slots (default 1); admission limits and workspace locks still apply; restart to apply changes", c.WorkerCapacity)
	if c.WorkerCapacity == 0 {
		message = "workerCapacity=0: draining; no new work admitted; restart to apply changes"
	}
	return doctorCheck{Name: "Service worker capacity", Passed: true, Message: message}
}
