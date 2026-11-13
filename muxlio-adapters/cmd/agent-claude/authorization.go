package main

import (
	"fmt"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// Exact runner builds qualify this mapping; an upgrade requires requalification.
func permissionArgs(p *contract.AuthorizationPolicy, version string) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("execution authorization denied: operator policy is required")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := supportedVersion(version); err != nil {
		return nil, err
	}

	if p.Mode == "restricted" {
		if p.Network != "none" || len(p.AllowedCommands) != 0 {
			return nil, fmt.Errorf("execution authorization: unsupported restricted network or command policy")
		}
		var tools []string
		for _, tool := range p.AllowedTools {
			switch tool {
			case "read-file":
				tools = append(tools, "Read", "Glob", "Grep")
			case "write-file":
				if !p.WorkspaceWrite {
					return nil, fmt.Errorf("write-file requires workspaceWrite")
				}
				tools = append(tools, "Edit", "Write")
			default:
				return nil, fmt.Errorf("execution authorization: restricted tool is not enforceable")
			}
		}
		toolList := strings.Join(tools, ",")
		return []string{"--restricted", "--safe-mode", "--strict-mcp-config", "--mcp-config", "{\"mcpServers\":{}}", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--disable-slash-commands", "--tools", toolList, "--allowedTools", toolList}, nil
	}

	return []string{"--permission-mode", "bypassPermissions"}, nil
}

// Headless, output, workspace and permission arguments are qualified together.
func supportedVersion(version string) error {
	switch version {
	case "2.1.319":
		return nil
	default:
		return fmt.Errorf("unsupported runner version %q: headless and permission flags are qualified for 2.1.319; install a qualified CLI build or update the adapter", version)
	}
}
