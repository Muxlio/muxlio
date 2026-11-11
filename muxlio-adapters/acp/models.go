package acp

import "encoding/json"

// SelectedModels reads current configuration, never the list of available
// choices. Selection does not establish which model served a request.
func SelectedModels(raw json.RawMessage) []string {
	var v struct {
		ConfigOptions []struct {
			Category     string          `json:"category"`
			CurrentValue json.RawMessage `json:"currentValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var models []string
	for _, c := range v.ConfigOptions {
		var id string
		if c.Category == "model" && json.Unmarshal(c.CurrentValue, &id) == nil && id != "" {
			models = append(models, id)
		}
	}
	return models
}
