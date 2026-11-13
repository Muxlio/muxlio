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
	if err := supportedVersion(version); err != nil {
		return nil, err
	}

	if p.Mode == "restricted" {
		return nil, fmt.Errorf("execution authorization: restrictive policy enforcement is not qualified for this runner; no generation started")
	}

	return []string{"--dangerously-skip-permissions"}, nil
}

// Headless, output, workspace and permission arguments are qualified together.
func supportedVersion(version string) error {
	switch version {
	case "1.5.5", "1.5.6", "1.5.8":
		return nil
	default:
		return fmt.Errorf("unsupported runner version %q: headless and permission flags are qualified for 1.5.5,1.5.6,1.5.8; install a qualified CLI build or update the adapter", version)
	}
}
