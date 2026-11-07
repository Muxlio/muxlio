// migrate-executor updates installed configuration during a bundled executor
// rename. Names are supplied by the installer; runtime components remain generic.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: migrate-executor CONFIG OLD NEW")
		os.Exit(2)
	}
	if err := migrateExecutor(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "executor configuration migration failed:", err)
		os.Exit(1)
	}
}

func migrateExecutor(path, old, next string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		return fmt.Errorf("configuration must be a JSON object")
	}
	changed := false
	if value, ok := config["capabilities"]; ok {
		var names []string
		if err := json.Unmarshal(value, &names); err != nil {
			return fmt.Errorf("capabilities must be an array of executor names")
		}
		found := false
		for _, name := range names {
			found = found || name == old
		}
		if found {
			seen := map[string]bool{}
			updated := []string{}
			for _, name := range names {
				if name == old {
					name = next
				}
				if !seen[name] {
					updated = append(updated, name)
					seen[name] = true
				}
			}
			config["capabilities"], _ = json.Marshal(updated)
			changed = true
		}
	}
	renameMap := func(object map[string]json.RawMessage, field string) error {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(value, &entries); err != nil || entries == nil {
			return fmt.Errorf("%s must be an object", field)
		}
		entry, found := entries[old]
		if !found {
			return nil
		}
		if existing, found := entries[next]; found {
			var a, b any
			if json.Unmarshal(entry, &a) != nil || json.Unmarshal(existing, &b) != nil {
				return fmt.Errorf("invalid executor settings")
			}
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			if !bytes.Equal(x, y) {
				return fmt.Errorf("conflicting old and new %s settings; resolve them before upgrading", field)
			}
		} else {
			entries[next] = entry
		}
		delete(entries, old)
		object[field], _ = json.Marshal(entries)
		changed = true
		return nil
	}
	if err := renameMap(config, "executors"); err != nil {
		return err
	}
	if value, ok := config["admission"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		var admission map[string]json.RawMessage
		if err := json.Unmarshal(value, &admission); err != nil || admission == nil {
			return fmt.Errorf("admission must be an object")
		}
		// Keep opaque capacity-group names unchanged. If an old executor used
		// its default group, retain that grouping explicitly after the rename.
		var caps map[string]json.RawMessage
		if err := json.Unmarshal(admission["executorCaps"], &caps); err != nil && len(admission["executorCaps"]) != 0 {
			return err
		}
		var demands map[string]json.RawMessage
		if err := json.Unmarshal(admission["executors"], &demands); err != nil && len(admission["executors"]) != 0 {
			return err
		}
		_, oldDemand := demands[old]
		if _, limited := caps[old]; limited && (changed || oldDemand) {
			var demand map[string]json.RawMessage
			if value := demands[old]; len(value) > 0 {
				if err := json.Unmarshal(value, &demand); err != nil {
					return err
				}
			}
			if demand == nil {
				demand = map[string]json.RawMessage{}
			}
			var group string
			if err := json.Unmarshal(demand["executorKey"], &group); err != nil && len(demand["executorKey"]) != 0 {
				return err
			}
			if group == "" {
				demand["executorKey"], _ = json.Marshal(old)
				if demands == nil {
					demands = map[string]json.RawMessage{}
				}
				demands[old], _ = json.Marshal(demand)
				admission["executors"], _ = json.Marshal(demands)
			}
		}
		if err := renameMap(admission, "executors"); err != nil {
			return err
		}
		config["admission"], _ = json.Marshal(admission)
	}
	if !changed {
		return nil
	}
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".executor-migration-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(updated, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
