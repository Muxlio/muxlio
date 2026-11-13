package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/muxlio/muxlio-core/internal/integrations"
)

func integrationBinary() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidate := filepath.Join(filepath.Dir(exe), "muxlio-mcp")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	if path, err := exec.LookPath("muxlio-mcp"); err == nil {
		absolute, _ := filepath.Abs(path)
		return absolute
	}
	return ""
}

func cmdIntegrations(args []string, dataDir string, globalJSON bool) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: muxlio integrations add|list|doctor|repair|remove --client NAME --scope project|user [--transport stdio|http]")
		return 2
	}
	action := args[0]
	switch action {
	case "add", "list", "doctor", "repair", "remove":
	default:
		fmt.Fprintln(os.Stderr, "unknown integration action")
		return 2
	}
	fs := flag.NewFlagSet("integrations "+action, flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	ws, _ := os.Getwd()
	o := integrations.Options{Home: home}
	fs.StringVar(&o.Client, "client", "", "authoring client: claude, codex (CLI and IDE), cursor, kiro")
	fs.StringVar(&o.Scope, "scope", "", "required: project or user")
	fs.StringVar(&o.Workspace, "workspace", ws, "project directory")
	fs.StringVar(&o.Transport, "transport", "stdio", "stdio or http")
	fs.StringVar(&o.Binary, "binary", integrationBinary(), "absolute muxlio-mcp executable for stdio")
	fs.StringVar(&o.Server, "server", "", "stdio coordinator URL or saved profile (default: saved defaultServer)")
	profile := fs.String("profile", "", "saved coordinator profile for stdio, or mcpProfiles entry for HTTP")
	fs.StringVar(&o.URL, "url", "", "HTTP MCP endpoint")
	fs.StringVar(&o.BearerEnv, "bearer-env", "", "environment variable holding HTTP bearer token; value is never stored")
	root := fs.String("data-dir", dataDir, "Muxlio runtime directory")
	jsonOut := fs.Bool("json", globalJSON, "JSON output (never config contents)")
	configOnly := fs.Bool("config-only", false, "doctor: inspect registration without starting a client session")
	if err := fs.Parse(reorderArgs(args[1:])); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected integration arguments")
		return 2
	}
	var err error
	o.Workspace, err = filepath.Abs(o.Workspace)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid workspace")
		return 2
	}
	if *root == "" {
		*root = defaultRuntime()
	}
	o.Config, err = filepath.Abs(filepath.Join(*root, "config.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid data directory")
		return 2
	}
	if *profile != "" {
		if o.Server != "" || o.URL != "" {
			fmt.Fprintln(os.Stderr, "--profile cannot be combined with --server or --url")
			return 2
		}
		if o.Transport == "http" {
			var cfg struct {
				MCPProfiles map[string]struct {
					URL       string `json:"url"`
					BearerEnv string `json:"bearerEnv"`
				} `json:"mcpProfiles"`
			}
			raw, readErr := os.ReadFile(o.Config)
			if readErr != nil || json.Unmarshal(raw, &cfg) != nil {
				fmt.Fprintln(os.Stderr, "cannot read MCP profiles from Muxlio config")
				return 1
			}
			p, ok := cfg.MCPProfiles[*profile]
			if !ok {
				fmt.Fprintln(os.Stderr, "unknown mcpProfiles entry")
				return 1
			}
			o.URL = p.URL
			if o.BearerEnv == "" {
				o.BearerEnv = p.BearerEnv
			}
		} else {
			o.Server = *profile
		}
	}
	clients := []string{o.Client}
	if o.Client == "" && action == "list" {
		clients = integrations.Clients
	}
	results := []integrations.Status{}
	for _, client := range clients {
		o.Client = client
		s, err := integrations.Apply(o, action)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		results = append(results, s)
		if action == "doctor" {
			if !s.Installed {
				fmt.Fprintln(os.Stderr, "Muxlio is missing; run integrations add with this client and scope")
				return 1
			}
			if !s.Managed {
				fmt.Fprintln(os.Stderr, "Muxlio entry is custom or edited; inspect the selected config before repair")
				return 1
			}
			if !*configOnly {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				err := integrations.VerifyClient(ctx, o)
				cancel()
				if err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					return 1
				}
			}
		}
	}
	if *jsonOut {
		raw, _ := json.Marshal(results)
		fmt.Println(string(raw))
	} else {
		for _, s := range results {
			fmt.Printf("%s (%s): installed=%t managed=%t changed=%t; %s\n", s.Client, s.Scope, s.Installed, s.Managed, s.Changed, s.Path)
		}
		if action == "doctor" && !*configOnly {
			fmt.Println("Client invoked server_health and get_workers successfully.")
		}
	}
	return 0
}
