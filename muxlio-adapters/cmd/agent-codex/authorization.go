package main

import (
	"fmt"

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
	switch version {
	case "0.167.0", "0.167.2":
	default:
		return nil, fmt.Errorf("execution authorization unsupported runner version %q", version)
	}
	if p.Mode == "restricted" {
		return nil, fmt.Errorf("execution authorization: restrictive tool, MCP and hook enforcement is not qualified for this runner; no generation started")
	}

	if p.Mode == "compatibility" {
		return []string{"--approve-for-me"}, nil
	}
	return []string{"--dangerously-bypass-approvals-and-sandbox"}, nil
}
