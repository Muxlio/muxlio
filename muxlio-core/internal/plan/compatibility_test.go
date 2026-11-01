package plan

import "testing"

func TestCheckCompatibility(t *testing.T) {
	if err := CheckCompatibility(CurrentIRVersion); err != nil {
		t.Fatalf("current IR should be supported: %v", err)
	}
	for _, version := range []string{"0.9", "1.1", "2.0", "next"} {
		if err := CheckCompatibility(version); err == nil {
			t.Errorf("IR %q should be rejected", version)
		}
	}
}

func TestVerifyDigest(t *testing.T) {
	p := &Plan{IRVersion: CurrentIRVersion, Name: "p", Root: "root", Subgraphs: map[string]Subgraph{"root": {}}}
	if _, err := p.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	if err := p.VerifyDigest(); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	p.Name = "changed"
	if err := p.VerifyDigest(); err == nil {
		t.Fatal("modified plan with stale digest accepted")
	}
}
