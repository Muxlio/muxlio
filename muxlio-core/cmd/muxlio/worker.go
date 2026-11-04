package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/muxlio/muxlio-core/internal/workers"
)

// adapterNameToCapability converts an adapter filename to its capability name.
// The mapping is the inverse of scriptPath: the first "-" becomes "/".
// Names without a dash are unchanged.
func adapterNameToCapability(name string) string {
	idx := strings.Index(name, "-")
	if idx < 0 {
		return name
	}
	return name[:idx] + "/" + name[idx+1:]
}

// discoverCapabilities scans dir for executable files and returns their
// capability names. Non-executable files and hidden files are skipped.
func discoverCapabilities(dir string) ([]workers.RemoteCapability, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading adapters directory %q: %w", dir, err)
	}
	var caps []workers.RemoteCapability
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		caps = append(caps, workers.RemoteCapability{Name: adapterNameToCapability(e.Name())})
	}
	return caps, nil
}

func cmdWorkerStart(coordinator, workerID, capabilities, capacityArg, adaptersDir, dataDir, workspace, _ string, workerTokenFlagVal, workerTokenFileFlagVal string) int {
	if coordinator == "" || workerID == "" {
		fmt.Fprintln(os.Stderr, "error: worker start requires --coordinator and --worker-id")
		fmt.Fprintln(os.Stderr, "usage: muxlio worker start --coordinator URL --worker-id ID --workspace DIR [--capabilities name1,name2] [--capacity N] [--worker-token TOKEN | --worker-token-file FILE]")
		return 2
	}
	if adaptersDir == "" {
		adaptersDir = filepath.Join(os.Getenv("HOME"), ".muxlio", "adapters")
	}
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("HOME"), ".muxlio")
	}
	if workspace == "" {
		fmt.Fprintln(os.Stderr, "error: worker start requires --workspace or MUXLIO_WORKSPACE")
		return 2
	}
	if !filepath.IsAbs(workspace) {
		fmt.Fprintln(os.Stderr, "error: workspace must be an absolute path")
		return 2
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "error: workspace must be an existing directory")
		return 2
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving workspace: %v\n", err)
		return 2
	}

	// Resolve worker token: flag > file > env > config.
	token := loadWorkerToken(workerTokenFlagVal, workerTokenFileFlagVal, dataDir)
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: worker start requires a worker token (--worker-token, --worker-token-file, MUXLIO_WORKER_TOKEN, or workerToken in config.json)")
		return 2
	}
	var caps []workers.RemoteCapability
	if capabilities == "" {
		// Auto-discover all executable adapters in the adapters directory.
		discovered, err := discoverCapabilities(adaptersDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if len(discovered) == 0 {
			fmt.Fprintf(os.Stderr, "error: no adapters found in %s; use --capabilities to specify them explicitly\n", adaptersDir)
			return 2
		}
		caps = discovered
		names := make([]string, len(caps))
		for i, c := range caps {
			names[i] = c.Name
		}
		fmt.Fprintf(os.Stderr, "auto-detected capabilities: %s\n", strings.Join(names, ", "))
	} else {
		for _, name := range strings.Split(capabilities, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				caps = append(caps, workers.RemoteCapability{Name: name})
			}
		}
		if len(caps) == 0 {
			fmt.Fprintln(os.Stderr, "error: at least one capability is required")
			return 2
		}
	}
	executor := workers.NewScriptExecutor(adaptersDir, filepath.Join(dataDir, "work"))
	settings, err := localExecutorSettings(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	executor.Executors = settings
	// Advertise only capabilities authorized by local operator policy.
	filtered := caps[:0]
	for _, cap := range caps {
		allowed, err := executor.CanExecute(cap.Name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if allowed {
			filtered = append(filtered, cap)
		}
	}
	if len(filtered) != len(caps) {
		fmt.Fprintln(os.Stderr, "capabilities omitted by local executor policy")
	}
	caps = filtered
	if len(caps) == 0 {
		fmt.Fprintln(os.Stderr, "error: no remotely authorized capabilities remain")
		return 2
	}

	// Parse capacity from the CLI, then the environment, or use the default.
	capacity := 1
	capStr := capacityArg
	if capStr == "" {
		capStr = os.Getenv("MUXLIO_WORKER_CAPACITY")
	}
	if capStr != "" {
		if cap, err := strconv.Atoi(capStr); err == nil && cap > 0 {
			capacity = cap
		} else {
			fmt.Fprintln(os.Stderr, "error: capacity must be a positive integer")
			return 2
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "muxlio worker %s connecting to %s (%s/%s, capacity=%d)\n", workerID, coordinator, runtime.GOOS, runtime.GOARCH, capacity)
	executor.Context = ctx
	executor.Workspace = workspace
	err = workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: coordinator, WorkerID: workerID, Capacity: capacity, Capabilities: caps, Token: token, WorkBase: filepath.Join(dataDir, "work"), Workspace: workspace}, executor)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker stopped:", err)
		return 1
	}
	return 0
}
