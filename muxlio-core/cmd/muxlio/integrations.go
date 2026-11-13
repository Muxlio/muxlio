package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/coordinatorconfig"
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
	if (action == "add" || action == "repair") && o.Transport == "stdio" && o.Server == "" {
		var cfg struct{ DefaultServer string }
		raw, err := os.ReadFile(o.Config)
		if err != nil || json.Unmarshal(raw, &cfg) != nil || cfg.DefaultServer == "" || strings.Contains(cfg.DefaultServer, "://") {
			fmt.Fprintln(os.Stderr, "stdio setup requires a saved profile; run muxlio init or specify --profile NAME")
			return 1
		}
		o.Server = cfg.DefaultServer
	}
	if (action == "add" || action == "repair") && o.Transport == "stdio" {
		if strings.Contains(o.Server, "://") {
			fmt.Fprintln(os.Stderr, "stdio registrations require a saved profile name; configure scoped credentials in servers and use --profile NAME")
			return 1
		}
		if _, _, err := coordinatorconfig.ResolveFile(o.Config, o.Server, "", ""); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	if action == "doctor" && o.Client == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		endpoint, err := integrations.CheckCoordinator(ctx, o.Config, o.Server, os.Getenv("MUXLIO_SERVER"), os.Getenv("MUXLIO_TOKEN"), !*configOnly)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if *jsonOut {
			raw, _ := json.Marshal(map[string]string{"endpoint": endpoint, "status": "ok"})
			fmt.Println(string(raw))
		} else {
			if *configOnly {
				fmt.Printf("Coordinator %s: endpoint configured and credential available (connection not checked).\n", endpoint)
			} else {
				fmt.Printf("Coordinator %s: connection and authenticated workers access verified.\n", endpoint)
			}
		}
		return 0
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
			ctx, cancelEndpoint := context.WithTimeout(context.Background(), 10*time.Second)
			err := integrations.VerifyEndpoint(ctx, o, !*configOnly)
			cancelEndpoint()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			if !*configOnly {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				err = integrations.VerifyClient(ctx, o)
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
