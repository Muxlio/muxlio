package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// CurrentVersion is the active CLI version string. It can be overridden at build time via -ldflags "-X main.CurrentVersion=...".
var CurrentVersion = "0.2.0"

func cmdUpgrade(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	prefix := fs.String("prefix", globalDataDir, "installation prefix directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}

	dest := *prefix
	if dest == "" {
		dest = defaultRuntime()
	}
	absDest, err := filepath.Abs(dest)
	if err == nil {
		dest = absDest
	}

	// Find build script
	repoDir := findRepoDir()
	if repoDir == "" {
		fmt.Fprintf(os.Stderr, "error: 'muxlio upgrade' builds release components from source and requires a repository checkout.\n\n")
		fmt.Fprintf(os.Stderr, "To upgrade:\n")
		fmt.Fprintf(os.Stderr, "  1. Navigate to your cloned muxlio repository directory: cd /path/to/muxlio\n")
		fmt.Fprintf(os.Stderr, "  2. Pull latest changes: git pull\n")
		fmt.Fprintf(os.Stderr, "  3. Run 'muxlio upgrade' (or 'bash scripts/upgrade.sh')\n")
		return 1
	}

	buildScript := filepath.Join(repoDir, "scripts", "build-release.sh")
	if !fileExists(buildScript) {
		fmt.Fprintf(os.Stderr, "error: build-release script not found at %s\n", buildScript)
		return 1
	}

	if !*jsonOut {
		fmt.Printf("Upgrading Muxlio in %s to version %s...\n", dest, CurrentVersion)
	}

	cmd := exec.Command("bash", buildScript, dest)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: upgrade build failed: %v\n", err)
		return 1
	}

	// Write version and upgrade timestamp
	versionPath := filepath.Join(dest, "version")
	_ = os.WriteFile(versionPath, []byte(fmt.Sprintf("%s\nupgraded=%s\n", CurrentVersion, time.Now().UTC().Format(time.RFC3339))), 0o644)

	// Refresh env file
	envScriptPath := filepath.Join(dest, "env")
	envContent := fmt.Sprintf(`# Muxlio shell environment
export MUXLIO_HOME="%s"
case ":$PATH:" in
  *":$MUXLIO_HOME/bin:"*) ;;
  *) export PATH="$MUXLIO_HOME/bin:$PATH" ;;
esac
`, dest)
	_ = os.WriteFile(envScriptPath, []byte(envContent), 0o600)

	if *jsonOut {
		out, _ := json.MarshalIndent(map[string]any{
			"upgraded": true,
			"version":  CurrentVersion,
			"prefix":   dest,
			"time":     time.Now().UTC().Format(time.RFC3339),
		}, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	fmt.Printf("\n✓ Upgrade complete! Installed version: %s\n", CurrentVersion)
	fmt.Printf("  Binary:  %s/bin/muxlio\n", dest)
	fmt.Printf("  Data:    %s/data (preserved)\n", dest)
	fmt.Printf("  Shell:   source %s\n", envScriptPath)
	return 0
}

func findRepoDir() string {
	// Try checking current directory or parent directories for package.json + DESIGN.md
	cwd, err := os.Getwd()
	if err == nil {
		dir := cwd
		for i := 0; i < 5; i++ {
			if fileExists(filepath.Join(dir, "package.json")) && fileExists(filepath.Join(dir, "DESIGN.md")) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Try checking relative to executable
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(filepath.Dir(exe))
		if fileExists(filepath.Join(dir, "package.json")) && fileExists(filepath.Join(dir, "DESIGN.md")) {
			return dir
		}
	}
	return ""
}
