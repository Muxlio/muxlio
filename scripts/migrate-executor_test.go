package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrateExecutorPreservesSettingsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"capabilities":["agent/old","custom/task","agent/new"],"executors":{"agent/old":{"allow":false,"env":{"EXAMPLE":"value"}},"custom/task":{"allow":true}},"admission":{"executorCaps":{"agent/old":2},"executors":{"agent/old":{"slots":2,"providerKey":"opaque-provider"}}},"token":"keep-token","custom":{"keep":true}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateExecutor(path, "agent/old", "agent/new"); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(path)
	var got struct {
		Capabilities []string                  `json:"capabilities"`
		Executors    map[string]map[string]any `json:"executors"`
		Admission    struct {
			ExecutorCaps map[string]int            `json:"executorCaps"`
			Executors    map[string]map[string]any `json:"executors"`
		} `json:"admission"`
		Token  string          `json:"token"`
		Custom map[string]bool `json:"custom"`
	}
	if err := json.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"agent/new", "custom/task"}) || got.Executors["agent/new"]["allow"] != false || got.Executors["agent/old"] != nil || got.Token != "keep-token" || !got.Custom["keep"] {
		t.Fatalf("lost settings: %+v", got)
	}
	demand := got.Admission.Executors["agent/new"]
	if demand["slots"] != float64(2) || demand["executorKey"] != "agent/old" || demand["providerKey"] != "opaque-provider" || got.Admission.ExecutorCaps["agent/old"] != 2 {
		t.Fatalf("lost admission budget: %+v", got.Admission)
	}
	if err := migrateExecutor(path, "agent/old", "agent/new"); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(updated) != string(again) {
		t.Fatal("repeated migration changed configuration")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe permissions: %v", info.Mode())
	}
}

func TestMigrateExecutorRejectsConflictsWithoutChangingConfig(t *testing.T) {
	for _, raw := range []string{
		`{"executors":{"agent/old":{"allow":false},"agent/new":{"allow":true}}}`,
		`{"capabilities":["agent/old"],"admission":{"executors":{"agent/old":{"slots":1},"agent/new":{"slots":2}}}}`,
		`{"capabilities":42}`,
		`{broken`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := migrateExecutor(path, "agent/old", "agent/new"); err == nil {
			t.Fatalf("accepted conflicting or invalid configuration: %s", raw)
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("failed migration changed original configuration")
		}
	}
}

func TestMigrateExecutorDoesNotCreateNewConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := migrateExecutor(path, "agent/old", "agent/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("fresh release acquired configuration")
	}
}

func TestMigrateExecutorPreservesImplicitCapacityGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"capabilities":["agent/old"],"admission":{"executorCaps":{"agent/old":1}}}`), 0600)
	if err := migrateExecutor(path, "agent/old", "agent/new"); err != nil {
		t.Fatal(err)
	}
	if err := migrateExecutor(path, "agent/old", "agent/new"); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &config)
	group := config["admission"].(map[string]any)["executors"].(map[string]any)["agent/new"].(map[string]any)["executorKey"]
	if group != "agent/old" {
		t.Fatalf("capacity group changed: %v", group)
	}
}
