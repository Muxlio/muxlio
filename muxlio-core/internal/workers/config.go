package workers

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ExecutorConfig is trusted, operator-owned configuration, never workflow input.
type ExecutorConfig struct {
	Allow *bool             `json:"allow,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	// SecretEnv maps opaque handles to operator environment variable names.
	SecretEnv map[string]string `json:"secretEnv,omitempty"`
}

func (c ExecutorConfig) ExplicitlyAllowed() bool { return c.Allow != nil && *c.Allow }

// ValidateExecutorConfigs validates the shared runtime configuration contract.
func ValidateExecutorConfigs(configs map[string]ExecutorConfig) error {
	for name, config := range configs {
		for handle, key := range config.SecretEnv {
			if handle == "" || key == "" || strings.ContainsAny(key, "=\x00") {
				return fmt.Errorf("invalid secret handle configuration")
			}
		}
		if err := ValidateWorkerEnv(config.Env); err != nil {
			return fmt.Errorf("executor %q: %w", name, err)
		}
	}
	return nil
}

// ValidateWorkerEnv validates operator-owned environment settings shared by
// native executors. Execution identity and scratch paths stay worker-managed.
func ValidateWorkerEnv(env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 || strings.HasPrefix(key, "MUXLIO_") || key == "HOME" || key == "TMPDIR" {
			return fmt.Errorf("invalid or reserved environment key %q", key)
		}
		if key == "PATH" {
			if value == "" {
				return fmt.Errorf("PATH must contain absolute directories")
			}
			for _, dir := range filepath.SplitList(value) {
				if !filepath.IsAbs(dir) {
					return fmt.Errorf("PATH must contain only absolute directories (no empty entries or shell variables)")
				}
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
	m, err := s.Descriptor(executor)
	if err != nil {
		return false, err
	}
	return !m.RequiresHostExecution || config.ExplicitlyAllowed(), nil
}
