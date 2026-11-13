package mcp

import "github.com/muxlio/muxlio-core/internal/coordinatorconfig"

func resolveCoordinator(target string) (string, string, error) {
	return coordinatorconfig.Resolve(target)
}
