package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthorizationValidationAndSummary(t *testing.T) {
	for _, mode := range []string{"", "auto", "yolo", "TRUSTED"} {
		p := &AuthorizationPolicy{Mode: mode, Network: "none", Isolation: "native"}
		if p.Validate() == nil {
			t.Fatalf("accepted mode %q", mode)
		}
	}
	p := &AuthorizationPolicy{Mode: "restricted", Network: "none", Isolation: "native", AllowedCommands: []string{"secret-command-text"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	summary := p.Summary()
	if strings.Contains(summary, "secret-command-text") || !strings.Contains(summary, "commands=1") {
		t.Fatal(summary)
	}
	p.AllowedCommands = []string{"changed"}
	if summary == p.Summary() {
		t.Fatal("digest did not change")
	}
	p.Mode = "trusted"
	if p.Validate() == nil {
		t.Fatal("trusted with restrictions accepted")
	}
	p.Mode = "restricted"
	p.Network = "auto"
	if p.Validate() == nil {
		t.Fatal("unknown network accepted")
	}
	p.Network = "none"
	p.Isolation = "maybe"
	if p.Validate() == nil {
		t.Fatal("unknown isolation accepted")
	}
	p.Isolation = "native"
	p.AllowedTools = []string{"provider-tool"}
	if p.Validate() == nil {
		t.Fatal("provider tool in generic contract")
	}
}

func TestAuthorizationRejectsUnknownFields(t *testing.T) {
	var policy AuthorizationPolicy
	for _, raw := range []string{`{"mode":"restricted","network":"none","isolation":"native","allowAll":true}`, `{"mode":"unknown","network":"none","isolation":"native"}`} {
		if json.Unmarshal([]byte(raw), &policy) == nil {
			t.Fatal("invalid operator policy accepted")
		}
	}
}

func TestAuthorizationRequiresExplicitGrants(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"restricted","isolation":"native"}`,
		`{"mode":"restricted","network":"none"}`,
		`{"mode":"trusted","network":"allowed","isolation":"native","workspaceWrite":true,"allowedTools":["*"]}`,
		`{"mode":"compatibility","network":"allowed","isolation":"native","allowedTools":["*"],"allowedCommands":["*"]}`,
		`{"mode":"restricted","network":"none","isolation":"native","allowedCommands":[""]}`,
	} {
		var policy AuthorizationPolicy
		if err := json.Unmarshal([]byte(raw), &policy); err == nil {
			t.Fatalf("incomplete policy accepted: %s", raw)
		}
	}
	var policy AuthorizationPolicy
	if err := json.Unmarshal([]byte(`{"mode":"restricted","network":"none","isolation":"native"}`), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.WorkspaceWrite || len(policy.AllowedTools) != 0 || len(policy.AllowedCommands) != 0 {
		t.Fatal("missing grants did not default to deny")
	}
}
