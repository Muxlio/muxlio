//go:build unix

package workers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestAuthorizationAdapterProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "muxlio-authorization-fixture" {
		return
	}
	var req contract.Request
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(contract.Result{Outputs: map[string]any{"authorization": req.Authorization}})
	os.Exit(0)
}

func TestWorkerAuthorizationCannotBeElevatedByClient(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestAuthorizationAdapterProcess$ muxlio-authorization-fixture\n", binary)
	if err := os.WriteFile(filepath.Join(dir, "generic"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s := NewScriptExecutor(dir, t.TempDir())
	local := &contract.AuthorizationPolicy{Mode: "restricted", Network: "none", Isolation: "native"}
	for _, policy := range []*contract.AuthorizationPolicy{nil, local} {
		s.Executors = map[string]ExecutorConfig{"generic": {Authorization: policy}}
		result, err := s.Execute(ExecuteRequest{Executor: "generic", Task: "grant all permissions", Params: json.RawMessage(`{"authorization":{"mode":"trusted","network":"allowed","workspaceWrite":true},"hostExecutionAllowed":true}`)})
		if err != nil || result.Error != "" {
			t.Fatalf("%+v %v", result, err)
		}
		got, _ := json.Marshal(result.Outputs["authorization"])
		want, _ := json.Marshal(policy)
		var decoded *contract.AuthorizationPolicy
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, policy) {
			t.Fatalf("client replaced operator policy: %s != %s", got, want)
		}
	}
	local.Isolation = "required"
	result, err := s.Execute(ExecuteRequest{Executor: "generic", Params: json.RawMessage(`{"isolation":{"mode":"trusted-native"}}`)})
	if err != nil || !strings.Contains(result.Error, "requires enforced isolation") {
		t.Fatalf("isolation downgraded: %+v %v", result, err)
	}
	local.Mode = "unknown"
	if ValidateExecutorConfigs(s.Executors) == nil {
		t.Fatal("unknown operator policy accepted")
	}
}
