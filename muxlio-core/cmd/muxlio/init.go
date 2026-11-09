package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func cmdInit(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent type to configure skill for (antigravity, codex, cursor, claude, kiro)")
	workspace := fs.String("workspace", "", "authorized workspace directory (default: current dir)")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	workerPath := fs.String("worker-path", "", "persist this absolute executable search path (default: preserve saved workerEnv.PATH, otherwise capture PATH)")
	mcpListen := fs.String("mcp-listen", "127.0.0.1:7602", "MCP HTTP listen address for setup (existing setting preserved unless this flag is supplied)")
	starter := fs.Bool("starter", false, "create a starter workflow in the workspace")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
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
	skillInstalledPaths := make([]string, 0)
	skillContent := getSkillContent(root)
	if len(skillContent) > 0 {
		globalSkill := filepath.Join(root, "skills", "SKILL.md")
		if err := os.WriteFile(globalSkill, skillContent, 0o600); err == nil {
			skillInstalledPaths = append(skillInstalledPaths, globalSkill)
		}

		// Workspace standard skill locations
		workspaceSkillsDir := filepath.Join(ws, ".skills", "muxlio")
		_ = os.MkdirAll(workspaceSkillsDir, 0o755)
		wsSkill := filepath.Join(workspaceSkillsDir, "SKILL.md")
		if err := os.WriteFile(wsSkill, skillContent, 0o644); err == nil {
			skillInstalledPaths = append(skillInstalledPaths, wsSkill)
		}

		// Agent-specific locations
		switch *agent {
		case "codex":
			codexDir := filepath.Join(ws, ".codex", "skills", "muxlio")
			_ = os.MkdirAll(codexDir, 0o755)
			codexSkill := filepath.Join(codexDir, "SKILL.md")
			_ = os.WriteFile(codexSkill, skillContent, 0o644)
			skillInstalledPaths = append(skillInstalledPaths, codexSkill)
		case "cursor":
			cursorDir := filepath.Join(ws, ".cursor", "rules")
			_ = os.MkdirAll(cursorDir, 0o755)
			cursorRule := filepath.Join(cursorDir, "muxlio.mdc")
			ruleContent := fmt.Sprintf("---\ndescription: Muxlio workflow authoring rules and conventions\nglobs: workflows/**,*.plan.json\n---\n\n%s", string(skillContent))
			_ = os.WriteFile(cursorRule, []byte(ruleContent), 0o644)
			skillInstalledPaths = append(skillInstalledPaths, cursorRule)
		case "claude":
			claudeDir := filepath.Join(ws, ".claude", "skills", "muxlio")
			_ = os.MkdirAll(claudeDir, 0o755)
			claudeSkill := filepath.Join(claudeDir, "SKILL.md")
			_ = os.WriteFile(claudeSkill, skillContent, 0o644)
			skillInstalledPaths = append(skillInstalledPaths, claudeSkill)
		case "antigravity":
			antigravityDir := filepath.Join(ws, ".agents", "skills", "muxlio")
			_ = os.MkdirAll(antigravityDir, 0o755)
			antigravitySkill := filepath.Join(antigravityDir, "SKILL.md")
			_ = os.WriteFile(antigravitySkill, skillContent, 0o644)
			skillInstalledPaths = append(skillInstalledPaths, antigravitySkill)
		}
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
			"initialized":   true,
			"runtimeDir":    root,
			"workspace":     ws,
			"configPath":    configPath,
			"configCreated": configCreated,
			"mcpEndpoint":   mcpEndpoint,
			"envScript":     envScriptPath,
			"skills":        skillInstalledPaths,
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

func getSkillContent(root string) []byte {
	// 1. Try canonical repo checkout: muxlio-skills/SKILL.md
	if repo := findRepoDir(); repo != "" {
		p := filepath.Join(repo, "muxlio-skills", "SKILL.md")
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			return data
		}
	}
	// 2. Try installed prefix relative to the executable: <prefix>/share/muxlio/skills/SKILL.md
	// This resolves the SKILL.md shipped alongside the binary (e.g. a Homebrew
	// install) in the same way adapters and UI assets are located.
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		prefix := filepath.Dir(filepath.Dir(exe))
		p := filepath.Join(prefix, "share", "muxlio", "skills", "SKILL.md")
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			return data
		}
	}
	// 3. Try runtime share: <root>/share/muxlio/skills/SKILL.md
	shareSkill := filepath.Join(root, "share", "muxlio", "skills", "SKILL.md")
	if data, err := os.ReadFile(shareSkill); err == nil && len(data) > 0 {
		return data
	}
	// 4. Try runtime skills: <root>/skills/SKILL.md
	runtimeSkill := filepath.Join(root, "skills", "SKILL.md")
	if data, err := os.ReadFile(runtimeSkill); err == nil && len(data) > 0 {
		return data
	}
	return nil
}
