package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestPreparedWorkspacePinSurvivesRecovery(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	p := &Plan{IRVersion: CurrentIRVersion, Name: "prepared", Root: "root", WorkspaceID: "prepared", WorkspaceDigest: digest,
		Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{{ID: "work", Kind: KindExecute, Executor: "generic"}}}}}
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
	store, executor := state.New(), workers.NewFake()
	engine := NewEngineWithStore(p, executor, store)
	id, _, err := engine.CreateRegisteredRun(raw, json.RawMessage(`{}`), "prepared", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewEngineWithStore(&Plan{}, executor, store).Run(id); err != nil {
		t.Fatal(err)
	}
	calls := executor.Calls()
	if len(calls) != 1 || calls[0].WorkspaceDigest != digest || calls[0].WorkspaceID != "prepared" {
		t.Fatalf("recovered execution lost workspace pin: %+v", calls)
	}
	p.WorkspaceDigest = "sha256:" + strings.Repeat("b", 64)
	if p.VerifyDigest() == nil {
		t.Fatal("plan identity did not bind prepared contents")
	}
	for _, value := range []string{"latest", "sha256:short", "sha256:" + strings.Repeat("A", 64)} {
		p.WorkspaceDigest = value
		if !HasErrors(Validate(p)) {
			t.Fatal("invalid content pin accepted")
		}
	}
	p.WorkspaceDigest, p.WorkspaceID = digest, ""
	if !HasErrors(Validate(p)) {
		t.Fatal("content pin without workspace authority accepted")
	}
}
