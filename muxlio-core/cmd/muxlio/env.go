package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdEnv(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	shell := fs.String("shell", "", "emit shell export commands (bash, zsh, fish)")
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

	binDir := filepath.Join(root, "bin")
	currentPath := os.Getenv("PATH")
	inPath := strings.Contains(":"+currentPath+":", ":"+binDir+":")

	// If shell output requested (or via --shell)
	if *shell != "" {
		switch *shell {
		case "fish":
			fmt.Printf("set -gx MUXLIO_HOME %q;\n", root)
			fmt.Printf("set -gx PATH %q $PATH;\n", binDir)
		default: // bash, zsh, sh
			fmt.Printf("export MUXLIO_HOME=%q\n", root)
			fmt.Printf("case \":$PATH:\" in\n  *\":%s:\"*) ;;\n  *) export PATH=\"%s:$PATH\" ;;\nesac\n", binDir, binDir)
		}
		return 0
	}

	dirs := map[string]string{
		"root":      root,
		"data":      filepath.Join(root, "data"),
		"logs":      filepath.Join(root, "logs"),
		"work":      filepath.Join(root, "work"),
		"artifacts": filepath.Join(root, "artifacts"),
		"bin":       binDir,
		"skills":    filepath.Join(root, "skills"),
		"adapters":  filepath.Join(root, "libexec", "muxlio", "adapters"),
		"ui":        filepath.Join(root, "share", "muxlio", "ui"),
		"config":    filepath.Join(root, "config.json"),
		"env":       filepath.Join(root, "env"),
	}

	dirStatus := make(map[string]bool)
	for k, path := range dirs {
		_, err := os.Stat(path)
		dirStatus[k] = (err == nil)
	}

	if *jsonOut {
		out, _ := json.MarshalIndent(map[string]any{
			"runtimeDir":  root,
			"binDir":      binDir,
			"inPath":      inPath,
			"directories": dirs,
			"status":      dirStatus,
		}, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	fmt.Println("Muxlio Environment:")
	fmt.Printf("  MUXLIO_HOME: %s\n", root)
	fmt.Printf("  Binary Dir:  %s\n", binDir)
	if inPath {
		fmt.Println("  PATH:        ✓ ~/.muxlio/bin is in your PATH")
	} else {
		fmt.Println("  PATH:        ✗ ~/.muxlio/bin is NOT in your PATH")
		fmt.Println("               Run: eval \"$(muxlio env --shell bash)\" or add to ~/.zshrc / ~/.bashrc")
	}

	fmt.Println("\nDirectory Layout:")
	for k, path := range dirs {
		mark := "✓"
		if !dirStatus[k] {
			mark = "✗ (missing)"
		}
		fmt.Printf("  %-10s %s  %s\n", k+":", mark, path)
	}

	fmt.Printf("\nTo configure your active shell:\n  eval \"$(muxlio env --shell zsh)\"\n")
	return 0
}
