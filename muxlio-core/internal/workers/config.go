package workers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ExecutorConfig is trusted, operator-owned configuration, never workflow input.
type ExecutorConfig struct {
	Allow *bool             `json:"allow,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
}

func (c ExecutorConfig) ExplicitlyAllowed() bool { return c.Allow != nil && *c.Allow }

// ValidateExecutorConfigs validates the shared runtime configuration contract.
func ValidateExecutorConfigs(configs map[string]ExecutorConfig) error {
	for name, config := range configs {
		for key, value := range config.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 || strings.HasPrefix(key, "MUXLIO_") {
				return fmt.Errorf("executor %q has invalid or reserved environment key %q", name, key)
			}
		}
	}
	return nil
}

// CanExecute consults an optional operator-installed descriptor beside the
// executable. Runtime permissions remain local, independent of workflow params.
func (s *ScriptExecutor) CanExecute(executor string) (bool, error) {
	if executor == "" || strings.Contains(executor, "..") || strings.ContainsAny(executor, "\\") {
		return false, fmt.Errorf("invalid executor name")
	}
	config := s.Executors[executor]
	if config.Allow != nil && !*config.Allow {
		return false, nil
	}
	raw, err := os.ReadFile(s.scriptPath(executor) + ".json")
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read executor descriptor: %w", err)
	}
	var descriptor struct {
		RequiresHostExecution bool `json:"requiresHostExecution"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return false, fmt.Errorf("invalid executor descriptor: %w", err)
	}
	return !descriptor.RequiresHostExecution || config.ExplicitlyAllowed(), nil
}
