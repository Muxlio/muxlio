package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestPlanRejectsLiteralCredentialsBeforePersistence(t *testing.T) {
	for _, params := range []string{`{"secrets":{"api":"raw-credential"}}`, `{"env":{"API_KEY":"raw-credential"}}`, `{"auth":{"password":"raw-credential"}}`} {
		p := &Plan{IRVersion: "1.0", Name: "security", Root: "root", Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{{ID: "test", Kind: "execute", Executor: "test", Params: json.RawMessage(params)}}}}}
		e := NewEngine(p, workers.NewFake())
		if _, err := e.CreateRun(json.RawMessage(`{}`)); err == nil || strings.Contains(err.Error(), "raw-credential") {
			t.Fatalf("unsafe plan accepted or error leaked: %v", err)
		}
		runs, _ := e.Store().ListRuns()
		if len(runs) != 0 {
			t.Fatal("credential-bearing plan created a durable run")
		}
	}
}

func TestPlanAcceptsHandlesAndRejectsNativeRestriction(t *testing.T) {
	node := Node{Kind: "execute", Executor: "test", Params: json.RawMessage(`{"secretRefs":{"api":"service"},"executionPolicy":{"allowedSecrets":["api"]}}`)}
	if err := validateExecutionSecurity(node); err != nil {
		t.Fatal(err)
	}
	node.Params = json.RawMessage(`{"executionPolicy":{"trust":"restricted"},"isolation":{"mode":"trusted-native"}}`)
	if err := validateExecutionSecurity(node); err == nil {
		t.Fatal("restricted plan accepted native execution")
	}
}
