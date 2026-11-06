package plan

import (
	"encoding/json"
	"strings"

	"github.com/muxlio/muxlio-core/internal/workers"
)

// Check conventional credential fields before a plan is pinned. Diagnostics
// deliberately contain neither the value nor its user-controlled field name.
// Arbitrary code and prose cannot be classified as safe: secrets must be supplied
// with secretRefs rather than placed in tasks, bindings or adapter parameters.
func hasLiteralCredentials(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			k := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch k {
			case "secretrefs", "allowedsecrets":
				continue
			case "secrets", "credentials", "authorization", "password", "apikey", "accesstoken", "refreshtoken", "token", "clientsecret":
				if item != nil && item != "" {
					return true
				}
			}
			if hasLiteralCredentials(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasLiteralCredentials(item) {
				return true
			}
		}
	}
	return false
}

func validateExecutionSecurity(node Node) error {
	var params struct {
		Policy     *workers.ExecutionPolicy `json:"executionPolicy"`
		SecretRefs map[string]string        `json:"secretRefs"`
	}
	if len(node.Params) != 0 {
		if err := json.Unmarshal(node.Params, &params); err != nil {
			return err
		}
	}
	_, err := workers.GuardRequest(workers.ExecuteRequest{Executor: node.Executor, Params: node.Params, Policy: params.Policy, SecretRefs: params.SecretRefs, WorkspaceMode: node.WorkspaceMode})
	return err
}
