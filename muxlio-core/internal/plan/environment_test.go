package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestPlanDigestBindsEnvironment(t *testing.T) {
	m := &contract.EnvironmentManifest{Version: "1", ProtocolDigest: "sha256:" + strings.Repeat("a", 64), Runners: map[string]string{"runner": "1.2.3"}}
	m.Digest = m.ComputeDigest()
	p := &Plan{IRVersion: CurrentIRVersion, Name: "pinned", Root: "root", Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{{ID: "work", Kind: KindExecute, Executor: "test", Environment: m}}}}}
	if HasErrors(Validate(p)) {
		t.Fatal(Validate(p))
	}
	if _, err := p.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Plan
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.VerifyDigest(); err != nil {
		t.Fatal(err)
	}
	pinned := decoded.Subgraphs["root"].Nodes[0].Environment
	pinned.Runners["runner"] = "2.0.0"
	if !HasErrors(Validate(&decoded)) {
		t.Fatal("invalid environment digest accepted")
	}
	pinned.Digest = pinned.ComputeDigest()
	if err := decoded.VerifyDigest(); err == nil {
		t.Fatal("plan digest did not bind changed environment")
	}
}
