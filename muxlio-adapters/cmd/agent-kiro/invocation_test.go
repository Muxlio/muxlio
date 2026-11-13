package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestNativeInvocation(t *testing.T) {
	for _, version := range []string{"2.37.0"} {
		for _, model := range []string{"", "explicit-model", "auto"} {
			args, err := invocationArgs(fixtureAuthorization(), version, model, "/scratch", "--prompt content")
			if err != nil {
				t.Fatal(err)
			}
			found := ""
			for i, arg := range args {
				if arg == "--model" {
					found = args[i+1]
				}
			}
			if found != model {
				t.Fatalf("model %q: %v", model, args)
			}
			if args[0] != "chat" {
				t.Fatal(args)
			}
		}
	}
	if args, err := invocationArgs(fixtureAuthorization(), "999.0.0", "", "/scratch", "prompt"); err == nil || len(args) != 0 || !strings.Contains(err.Error(), "install") {
		t.Fatalf("unsupported version: %v %v", args, err)
	}
}

func TestNativeDefaultsAndModelProvenance(t *testing.T) {
	binary := buildAgentKiro(t)
	for _, tc := range []struct{ model, diagnostic, hint string }{
		{"", "", ""}, {"explicit-model", "", ""},
		{"", "unknown option --headless", "adapter-qualified"},
		{"explicit-model", "authentication failed", "login"},
		{"", "stdout:unknown option --headless", "adapter-qualified"},
		{"explicit-model", "stdout:authentication failed", "login"},
		{"", "stdout:permission denied", "operator authorization"},
	} {
		model := tc.model
		t.Run("model="+model+tc.diagnostic, func(t *testing.T) {
			dir := t.TempDir()
			cli := filepath.Join(dir, "kiro-cli")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.37.0; exit 0; fi\nprintf '%s\\n' \"$@\" > " + dir + "/args\ncat <<'EVENTS'\n{\"type\":\"runFinished\",\"data\":{}}\nEVENTS\n"
			if tc.diagnostic != "" {
				redirect := " >&2"
				if strings.HasPrefix(tc.diagnostic, "stdout:") {
					redirect = ""
				}
				script += "echo '" + strings.TrimPrefix(tc.diagnostic, "stdout:") + "'" + redirect + "\nexit 1\n"
			}
			if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			params, _ := json.Marshal(map[string]string{"model": model})
			// Cover an entirely unconfigured adapter as well as an explicit empty model.
			if model == "" && tc.diagnostic == "" {
				params = nil
			}
			input, _ := json.Marshal(adapter.Request{Authorization: fixtureAuthorization(), Executor: "agent/kiro", Workspace: dir, WorkDir: dir, Task: "noop", Params: params})
			cmd := exec.Command(binary)
			cmd.Env = append(os.Environ(), "PATH="+dir+":/bin:/usr/bin", "KIRO_CLI="+cli)
			cmd.Stdin = strings.NewReader(string(input))
			raw, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v: %s", err, raw)
			}
			var response adapter.Response
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatalf("%v: %s", err, raw)
			}
			if tc.hint == "" && response.Error != "" {
				t.Fatal(response.Error)
			}
			if tc.hint != "" && !strings.Contains(response.Error, tc.hint) {
				t.Fatalf("missing guidance: %s", raw)
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(args), "--model\n") != (model != "") {
				t.Fatalf("model %q: %s", model, args)
			}
			if response.Accounting == nil {
				t.Fatal("missing accounting")
			}
			for _, entry := range response.Accounting.Entries {
				if len(entry.Costs) != 0 {
					t.Fatalf("cost inferred without monetary evidence: %+v", entry)
				}
			}
			var requested []string
			for _, m := range response.Accounting.Models {
				if m.Role == "requested" {
					requested = append(requested, m.ID)
					if m.Source != "adapter-params" {
						t.Fatal(m)
					}
				}
				if m.Role == "served" && m.ID == model {
					t.Fatal("request treated as served model")
				}
			}
			var want []string
			if model != "" {
				want = []string{model}
			}
			if !reflect.DeepEqual(requested, want) {
				t.Fatalf("requested=%v want=%v", requested, want)
			}
		})
	}
}

func TestNativeReadinessRejectsUnsupportedVersion(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "runner")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 999.0.0; else echo 'not logged in'; fi\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	checks := runnerChecks(cli, "status")
	var versionFailed, loginFailed bool
	for _, c := range checks {
		if c.Name == contract.ProbeVersion {
			versionFailed = !c.Passed && strings.Contains(c.Message, "install")
		}
		if c.Name == contract.ProbeAuthentication {
			loginFailed = !c.Passed && strings.Contains(c.Message, "login")
		}
	}
	if !versionFailed || !loginFailed {
		t.Fatalf("%+v", checks)
	}
}
