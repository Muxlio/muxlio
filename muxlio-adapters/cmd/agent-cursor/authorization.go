package main

import (
	"fmt"

	"github.com/muxlio/muxlio-adapters/contract"
)

// Explicit runner pins are enforced by the environment contract, not this permission mapping.
func permissionArgs(p *contract.AuthorizationPolicy, _ string) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("execution authorization denied: operator policy is required")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	if p.Mode == "restricted" {
		return nil, fmt.Errorf("execution authorization: restrictive policy enforcement is not qualified for this runner; no generation started")
	}

	return []string{"--yolo", "--trust"}, nil
}
