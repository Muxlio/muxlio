package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/muxlio/muxlio-core/internal/integrations"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func cmdInit(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	agent := fs.String("agent", "", "compatible alias for --client (authoring client, independent of execution adapters)")
	client := fs.String("client", "", "authoring client: claude, codex, cursor, kiro; antigravity instructions only")
	scope := fs.String("scope", "project", "client registration scope: project or user (init defaults to project)")
	mcpBinary := fs.String("mcp-binary", integrationBinary(), "absolute muxlio-mcp executable for client registration")
	workspace := fs.String("workspace", "", "authorized workspace directory (default: current dir)")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	workerPath := fs.String("worker-path", "", "persist this absolute executable search path (default: preserve saved workerEnv.PATH, otherwise capture PATH)")
	mcpListen := fs.String("mcp-listen", "127.0.0.1:7602", "MCP HTTP listen address for setup (existing setting preserved unless this flag is supplied)")
	starter := fs.Bool("starter", false, "create a starter workflow in the workspace")
	allowCommand := fs.Bool("allow-command", false, "authorize command execution for workflow checks and local commits")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *client != "" {
		if *agent != "" && *agent != *client {
			fmt.Fprintln(os.Stderr, "error: --client and --agent must agree")
			return 2
		}
		*agent = *client
	}
	if *scope != "user" && *scope != "project" {
		fmt.Fprintln(os.Stderr, "error: --scope must be project or user")
		return 2
	}
	switch *agent {
	case "", "antigravity", "claude", "codex", "cursor", "kiro":
	default:
		fmt.Fprintf(os.Stderr, "error: unknown agent %q\n", *agent)
		return 1
	}

	if _, port, err := net.SplitHostPort(*mcpListen); err != nil || port == "" {
		fmt.Fprintln(os.Stderr, "error: --mcp-listen must be host:port")
		return 2
	}
	setMCPListen, setWorkerPath, setWorkspace := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mcp-listen":
			setMCPListen = true
		case "worker-path":
			setWorkerPath = true
		case "workspace":
			setWorkspace = true
		}
	})
	root := *dataDir
	if root == "" {
		root = defaultRuntime()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid data-dir path: %v\n", err)
		return 1
	}
	root = absRoot

	ws := *workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid workspace path: %v\n", err)
		return 1
	}
	ws = absWS

	// 1. Create .muxlio standard directory tree with 0700 permissions
	dirs := []string{
		root,
		filepath.Join(root, "data"),
		filepath.Join(root, "logs"),
		filepath.Join(root, "work"),
		filepath.Join(root, "artifacts"),
		filepath.Join(root, "bin"),
		filepath.Join(root, "skills"),
		filepath.Join(root, "libexec", "muxlio", "adapters"),
		filepath.Join(root, "share", "muxlio", "ui"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "error: creating directory %s: %v\n", d, err)
			return 1
		}
	}
	_ = os.Chmod(root, 0o700)

	// 2. Write config.json if not present
	configPath := filepath.Join(root, "config.json")
	configCreated := false
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		cfg := map[string]any{
			"runtimeDir":            root,
			"addr":                  "127.0.0.1:7600",
			"workspace":             ws,
			"capabilities":          BundledExecutorCapabilities,
			"attemptTimeoutSeconds": 600,
		}
		raw, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(configPath, raw, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing config.json: %v\n", err)
			return 1
		}
		configCreated = true
	}
	// Register the service-owned MCP listener without replacing existing settings.
	configRaw, err := os.ReadFile(configPath)
	var config map[string]json.RawMessage
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: reading config:", err)
		return 1
	}
	if err := json.Unmarshal(configRaw, &config); err != nil || config == nil {
		fmt.Fprintln(os.Stderr, "error: config must be a JSON object")
		return 1
	}
	configChanged := false
	if *allowCommand {
		executors := map[string]json.RawMessage{}
		if raw := config["executors"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &executors); err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid executors configuration:", err)
				return 1
			}
		}
		if executors == nil {
			executors = map[string]json.RawMessage{}
		}
		command := map[string]json.RawMessage{}
		if raw := executors["command"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &command); err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid command executor configuration:", err)
				return 1
			}
		}
		if command == nil {
			command = map[string]json.RawMessage{}
		}
		command["allow"] = json.RawMessage("true")
		executors["command"], _ = json.Marshal(command)
		config["executors"], _ = json.Marshal(executors)
		configChanged = true
	}
	if setWorkspace {
		value, _ := json.Marshal(ws)
		if !bytes.Equal(bytes.TrimSpace(config["workspace"]), value) {
			config["workspace"] = value
			configChanged = true
		}
	} else if raw := config["workspace"]; len(raw) > 0 {
		// Repeated setup preserves the selected workspace and installs skills
		// there, even when invoked from a different working directory.
		if err := json.Unmarshal(raw, &ws); err != nil {
			fmt.Fprintln(os.Stderr, "error: workspace must be a string")
			return 1
		}
	}
	if _, exists := config["mcp"]; !exists || setMCPListen {
		mcpConfig := map[string]json.RawMessage{}
		if raw := config["mcp"]; len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &mcpConfig); err != nil || mcpConfig == nil {
				fmt.Fprintln(os.Stderr, "error: mcp config must be an object or null")
				return 1
			}
		}
		mcpConfig["listen"], _ = json.Marshal(*mcpListen)
		config["mcp"], _ = json.Marshal(mcpConfig)
		configChanged = true
	}
	workerEnv := map[string]string{}
	if raw := config["workerEnv"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &workerEnv); err != nil {
			fmt.Fprintln(os.Stderr, "error: invalid workerEnv:", err)
			return 1
		}
		if workerEnv == nil {
			workerEnv = map[string]string{}
		}
	}
	if _, hasPath := workerEnv["PATH"]; !hasPath || setWorkerPath {
		value := os.Getenv("PATH")
		if setWorkerPath {
			value = *workerPath
		}
		workerEnv["PATH"] = value
		configChanged = true
	}
	if err := workers.ValidateWorkerEnv(workerEnv); err != nil {
		fmt.Fprintln(os.Stderr, "error: workerEnv:", err)
		return 1
	}
	config["workerEnv"], _ = json.Marshal(workerEnv)

	var mcpSetup *serviceMCPConfig
	if err := json.Unmarshal(config["mcp"], &mcpSetup); err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid MCP configuration:", err)
		return 1
	}
	mcpEndpoint := ""
	if mcpSetup != nil {
		if _, port, err := net.SplitHostPort(mcpSetup.Listen); err != nil || port == "" {
			fmt.Fprintln(os.Stderr, "error: mcp.listen must be host:port")
			return 1
		}
		scheme := "http"
		if mcpSetup.TLSCert != "" {
			scheme = "https"
		}
		mcpEndpoint = scheme + "://" + mcpSetup.Listen + "/mcp"
	}
	if configChanged {
		raw, _ := json.MarshalIndent(config, "", "  ")
		if err := writeInitConfig(configPath, raw); err != nil {
			fmt.Fprintln(os.Stderr, "error: configuring worker environment:", err)
			return 1
		}
	}
	if _, err := ensureAuthToken(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: creating coordinator token: %v\n", err)
		return 1
	}
	if _, err := ensureWorkerToken(configPath); err != nil {
		fmt.Fprintln(os.Stderr, "error: creating worker token:", err)
		return 1
	}

	profile, err := ensureLocalCoordinatorProfile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: configuring coordinator profile:", err)
		return 1
	}

	var registration *integrations.Status
	if *agent != "" && *agent != "antigravity" {
		home, _ := os.UserHomeDir()
		status, err := integrations.Apply(integrations.Options{Client: *agent, Scope: *scope, Home: home, Workspace: ws, Transport: "stdio", Server: profile, Binary: *mcpBinary, Config: configPath}, "repair")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: client registration:", err)
			return 1
		}
		registration = &status
	}

	// 3. Write env script for shell sourcing
	envScriptPath := filepath.Join(root, "env")
	envContent := fmt.Sprintf(`# Muxlio shell environment
export MUXLIO_HOME="%s"
case ":$PATH:" in
  *":$MUXLIO_HOME/bin:"*) ;;
  *) export PATH="$MUXLIO_HOME/bin:$PATH" ;;
esac
`, root)
	_ = os.WriteFile(envScriptPath, []byte(envContent), 0o600)

	// 4. Install Agent Skill
	instructionBase := ws
	if *scope == "user" {
		instructionBase, err = os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: locating user instructions directory")
			return 1
		}
	}
	skillInstalledPaths := []string{}
	skillPreservedPaths := []string{}
	if source := getSkillDir(root); source != "" {
		result, err := integrations.InstallSkill(source, filepath.Join(root, "skills"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: runtime skill:", err)
			return 1
		}
		skillInstalledPaths = append(skillInstalledPaths, result.Installed...)
		skillPreservedPaths = append(skillPreservedPaths, result.Preserved...)
		result, err = integrations.InstallClientSkill(source, instructionBase, *agent, *scope)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: client skill:", err)
			return 1
		}
		skillInstalledPaths = append(skillInstalledPaths, result.Installed...)
		skillPreservedPaths = append(skillPreservedPaths, result.Preserved...)
	} else if *agent != "" {
		fmt.Fprintln(os.Stderr, "error: Muxlio skill bundle unavailable; reinstall the Muxlio distribution")
		return 1
	}

	// 5. Create starter sample workflow if requested
	starterPlanPath := ""
	starterInputPath := ""
	if *starter {
		workflowsDir := filepath.Join(ws, "workflows")
		_ = os.MkdirAll(workflowsDir, 0o755)
		starterPlanPath = filepath.Join(workflowsDir, "sample.plan.json")
		starterInputPath = filepath.Join(workflowsDir, "sample.input.json")
		samplePlan := `{
  "irVersion": "1.0",
  "name": "sample-workflow",
  "root": "root",
  "subgraphs": {
    "root": {
      "nodes": [
        {
          "id": "step1",
          "kind": "execute",
          "executor": "agent/codex",
          "task": "Process inputs and generate summary.md",
          "outputs": {
            "summary": { "mime": "text/markdown", "path": "summary.md" }
          }
        }
      ],
      "output": {
        "summary": { "ref": { "node": "step1", "port": "summary" } }
      }
    }
  }
}`
		_ = os.WriteFile(starterPlanPath, []byte(samplePlan), 0o644)
		_ = os.WriteFile(starterInputPath, []byte(`{"message": "Hello from Muxlio!"}`), 0o644)
	}

	if *jsonOut {
		res := map[string]any{
			"initialized":     true,
			"runtimeDir":      root,
			"workspace":       ws,
			"configPath":      configPath,
			"configCreated":   configCreated,
			"mcpEndpoint":     mcpEndpoint,
			"envScript":       envScriptPath,
			"skills":          skillInstalledPaths,
			"skillsPreserved": skillPreservedPaths,
		}
		if registration != nil {
			res["integration"] = registration
		}
		if starterPlanPath != "" {
			res["starterPlan"] = starterPlanPath
			res["starterInput"] = starterInputPath
		}
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	fmt.Println("✓ Muxlio environment initialized successfully!")
	fmt.Printf("  Runtime dir: %s\n", root)
	fmt.Printf("  Workspace:   %s\n", ws)
	if registration != nil {
		fmt.Printf("  Client MCP:  %s (%s): %s\n", registration.Client, registration.Scope, registration.Path)
	}
	if configCreated {
		fmt.Printf("  Config:      %s (created)\n", configPath)
	} else {
		fmt.Printf("  Config:      %s (existing)\n", configPath)
	}
	if len(skillInstalledPaths) > 0 {
		fmt.Println("  Agent skill installed:")
		for _, p := range skillInstalledPaths {
			fmt.Printf("    • %s\n", p)
		}
	}
	for _, p := range skillPreservedPaths {
		fmt.Printf("  Preserved skill material: %s\n", p)
	}
	if starterPlanPath != "" {
		fmt.Printf("  Starter:     %s\n", starterPlanPath)
	}
	if mcpEndpoint != "" {
		fmt.Printf("  MCP HTTP:    %s (starts with muxlio service start)\n", mcpEndpoint)
	} else {
		fmt.Println("  MCP HTTP:    disabled in config")
	}
	fmt.Println("  Authentication: persistent client and worker tokens are stored in the protected config or configured token files.")
	fmt.Println("\nTo configure your shell PATH:")
	fmt.Printf("  source %s\n", envScriptPath)
	return 0
}

// Persist setup changes atomically so service restarts cannot read partial JSON.
func writeInitConfig(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".muxlio-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Locate the whole bundle so relative references work outside a source checkout.
func getSkillDir(root string) string {
	candidates := []string{}
	// Prefer distributed assets: an unrelated target repository must not replace
	// installed instructions (including user-scope instructions) with its files.
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "muxlio", "skills"))
	}
	candidates = append(candidates, filepath.Join(root, "share", "muxlio", "skills"))
	// Source checkouts need not contain the historical DESIGN.md marker used
	// by the upgrade command's repository lookup.
	if cwd, err := os.Getwd(); err == nil {
		for dir, depth := cwd, 0; depth < 5; dir, depth = filepath.Dir(dir), depth+1 {
			module, err := os.ReadFile(filepath.Join(dir, "muxlio-core", "go.mod"))
			if err == nil && bytes.HasPrefix(module, []byte("module github.com/muxlio/muxlio-core\n")) {
				candidates = append(candidates, filepath.Join(dir, "muxlio-skills"))
			}
		}
	}
	candidates = append(candidates, filepath.Join(root, "skills"))
	for _, dir := range candidates {
		if raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil && len(raw) > 0 {
			return dir
		}
	}
	return ""
}
