package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func fixtureAuthorization() *contract.AuthorizationPolicy {
	return &contract.AuthorizationPolicy{Mode: "trusted", AllowedTools: []string{"*"}, AllowedCommands: []string{"*"}, WorkspaceWrite: true, Network: "allowed", Isolation: "native"}
}

func versionedFixture(script string) string {
	return strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.5.8; exit 0; fi\n", 1)
}

func TestPermissionArguments(t *testing.T) {
	for _, version := range []string{"1.5.5", "1.5.6", "1.5.8"} {
		t.Run(version, func(t *testing.T) {
			p := fixtureAuthorization()
			got, err := permissionArgs(p, version)
			want := []string{"--dangerously-skip-permissions"}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("trusted: %v %v", got, err)
			}
			p.Mode = "compatibility"
			got, err = permissionArgs(p, version)

			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("compatibility: %v %v", got, err)
			}
			for _, mode := range []string{"trusted", "restricted", "compatibility", "unknown"} {
				p.Mode = mode
				if args, err := permissionArgs(p, "999.0.0"); err == nil || len(args) != 0 {
					t.Fatalf("unknown version accepted: %v %v", args, err)
				}
			}
			p.Mode = "unknown"
			if args, err := permissionArgs(p, version); err == nil || len(args) != 0 {
				t.Fatal("unknown mode accepted")
			}
			if args, err := permissionArgs(nil, version); err == nil || len(args) != 0 {
				t.Fatal("missing policy accepted")
			}
			p = &contract.AuthorizationPolicy{Mode: "restricted", Network: "none", Isolation: "native"}
			got, err = permissionArgs(p, version)
			if err == nil || len(got) != 0 {
				t.Fatalf("unenforceable restrictive policy accepted: %v %v", got, err)
			}

		})
	}
}
