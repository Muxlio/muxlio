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
	case "2026.11.05-86047df", "2026.11.11-b912975":
	default:
		return nil, fmt.Errorf("execution authorization unsupported runner version %q", version)
	}
	if p.Mode == "restricted" {
		return nil, fmt.Errorf("execution authorization: restrictive policy enforcement is not qualified for this runner; no generation started")
	}

	return []string{"--yolo", "--trust"}, nil
}
