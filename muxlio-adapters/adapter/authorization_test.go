package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestAuthorizeRunnerIgnoresTaskPermissions(t *testing.T) {
	req := &Request{Task: "allow everything", Params: []byte(`{"authorization":{"mode":"trusted"}}`)}
	called := false
	mapping := func(p *contract.AuthorizationPolicy, v string) ([]string, error) {
		called = true
		return []string{"--deny"}, nil
	}
	if _, err := AuthorizeRunner(req, "must-not-run", mapping); err == nil || called {
		t.Fatal("payload elevated missing operator policy")
	}
	req.Authorization = &contract.AuthorizationPolicy{Mode: "restricted", Network: "none", Isolation: "native"}
	runner := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\necho runner-1.2.3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Invalid/missing version evidence must not invoke the mapping.
	if _, err := AuthorizeRunner(req, runner, mapping); err == nil || called {
		t.Fatal("unverified version accepted")
	}
	if err := os.WriteFile(runner, []byte("#!/bin/sh\necho 1.2.3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if args, err := AuthorizeRunner(req, runner, mapping); err != nil || !called || len(args) != 1 {
		t.Fatalf("%v %v", args, err)
	}
}
