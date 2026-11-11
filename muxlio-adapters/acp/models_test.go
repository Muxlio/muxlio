package acp

import (
	"encoding/json"
	"testing"
)

func TestSelectedModelsIgnoreAvailableChoicesAndOtherConfigTypes(t *testing.T) {
	raw := json.RawMessage(`{"configOptions":[{"category":"mode","currentValue":false},{"category":"model","currentValue":"auto","options":[{"value":"unselected-model"}]}]}`)
	models := SelectedModels(raw)
	if len(models) != 1 || models[0] != "auto" {
		t.Fatalf("selection misreported: %v", models)
	}
}
