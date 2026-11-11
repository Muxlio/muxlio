package acp

import "encoding/json"

// selectedModelsAtSessionSetup reports model selections after session/new,
// preferring configOptions from the response and otherwise the last buffered
// config_option_update received before the response arrived.
func selectedModelsAtSessionSetup(configOptions json.RawMessage, pending []message) []string {
	if len(configOptions) > 0 && string(configOptions) != "null" {
		raw, _ := json.Marshal(map[string]json.RawMessage{"configOptions": configOptions})
		if models := SelectedModels(raw); len(models) > 0 {
			return models
		}
	}
	var models []string
	for _, m := range pending {
		var params struct {
			Update json.RawMessage `json:"update"`
		}
		if json.Unmarshal(m.Params, &params) != nil {
			continue
		}
		if next := SelectedModels(params.Update); len(next) > 0 {
			models = next
		}
	}
	return models
}

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
