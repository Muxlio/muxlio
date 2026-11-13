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
	return strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.1.319; exit 0; fi\n", 1)
}

func TestPermissionArguments(t *testing.T) {
	for _, version := range []string{"2.1.319"} {
		t.Run(version, func(t *testing.T) {
			p := fixtureAuthorization()
			got, err := permissionArgs(p, version)
			want := []string{"--permission-mode", "bypassPermissions"}
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
			want = []string{"--restricted", "--safe-mode", "--strict-mcp-config", "--mcp-config", "{\"mcpServers\":{}}", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--disable-slash-commands", "--tools", "", "--allowedTools", ""}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("restricted: %v %v", got, err)
			}
			p.AllowedTools = []string{"shell"}
			if args, err := permissionArgs(p, version); err == nil || len(args) != 0 {
				t.Fatal("unsupported shell permission accepted")
			}
			p.AllowedTools = []string{"write-file"}
			if _, err := permissionArgs(p, version); err == nil {
				t.Fatal("write tool without write permission accepted")
			}
			p.WorkspaceWrite = true
			if args, err := permissionArgs(p, version); err != nil || !strings.Contains(strings.Join(args, " "), "Edit,Write") {
				t.Fatalf("write tools: %v %v", args, err)
			}
			p.AllowedCommands = []string{"go test ./..."}
			if _, err := permissionArgs(p, version); err == nil {
				t.Fatal("command allowlist silently ignored")
			}

		})
	}
}
