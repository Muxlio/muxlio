package acp

import (
	"encoding/json"
	"testing"
)

func TestSelectedModelsAtSessionSetupPrefersResponseOverPending(t *testing.T) {
	pending := []message{{
		Params: json.RawMessage(`{"sessionId":"session","update":{"sessionUpdate":"config_option_update","configOptions":[{"category":"model","currentValue":"pending"}]}}`),
	}}
	if models := selectedModelsAtSessionSetup(json.RawMessage(`[{"category":"model","currentValue":"response"}]`), pending); len(models) != 1 || models[0] != "response" {
		t.Fatalf("response configOptions ignored: %v", models)
	}
	if models := selectedModelsAtSessionSetup(nil, pending); len(models) != 1 || models[0] != "pending" {
		t.Fatalf("pending config_option_update lost: %v", models)
	}
}

func TestSelectedModelsIgnoreAvailableChoicesAndOtherConfigTypes(t *testing.T) {
	raw := json.RawMessage(`{"configOptions":[{"category":"mode","currentValue":false},{"category":"model","currentValue":"auto","options":[{"value":"unselected-model"}]}]}`)
	models := SelectedModels(raw)
	if len(models) != 1 || models[0] != "auto" {
		t.Fatalf("selection misreported: %v", models)
	}
}
