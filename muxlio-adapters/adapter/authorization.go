package adapter

import (
	"fmt"

	"github.com/muxlio/muxlio-adapters/contract"
)

type AuthorizationMapping func(*contract.AuthorizationPolicy, string) ([]string, error)

// AuthorizeRunner validates local policy and observes the actual runner version
// before any generation. Provider mappings belong to each adapter executable.
func AuthorizeRunner(req *Request, binary string, mapping AuthorizationMapping) ([]string, error) {
	if err := req.Authorization.Validate(); err != nil {
		return nil, err
	}
	// Record denied attempts too, without serializing operator-supplied text.
	AppendLogChunk("stderr", req.Authorization.Summary())
	if req.Authorization == nil {
		return nil, fmt.Errorf("execution authorization denied: operator policy is required")
	}
	check := CommandCheck(contract.ProbeVersion, binary, "--version")
	if !check.Passed {
		return nil, fmt.Errorf("execution authorization cannot verify runner version")
	}
	args, err := mapping(req.Authorization, check.Version)
	if err != nil {
		return nil, err
	}
	return args, nil
}
