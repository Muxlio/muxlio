package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/workers"
)

const workspaceUsage = `Usage:
  muxlio workspace add <id> <absolute-path> --worker-id <id> [--digest sha256:...] [--alias <alias>...]
  muxlio workspace list [--json]
  muxlio workspace show <id> [--json]
  muxlio workspace remove <id>
  muxlio workspace digest <prepared-directory>

Registry commands use the local config selected by MUXLIO_CONFIG or --data-dir (default: MUXLIO_HOME or ~/.muxlio).
`

type workspaceEntry struct {
	ID string `json:"id"`
	workers.WorkspaceBinding
}

func cmdWorkspace(args []string, dataDir string, jsonOut bool) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, workspaceUsage)
		return 2
	}
	action := args[0]
	if action == "digest" {
		return cmdWorkspaceDigest(args[1:])
	}
	fs := flag.NewFlagSet("workspace "+action, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), workspaceUsage) }
	var binding workers.WorkspaceBinding
	var positional int
	switch action {
	case "add":
		positional = 2
		fs.StringVar(&binding.WorkerID, "worker-id", "", "Owning worker identity")
		fs.Func("digest", "Prepared workspace digest", func(digest string) error {
			if digest == "" {
				return errors.New("digest must be a SHA-256 content digest")
			}
			binding.Digest = digest
			return nil
		})
		fs.Func("alias", "Additional workspace identity (repeatable)", func(alias string) error {
			binding.Aliases = append(binding.Aliases, alias)
			return nil
		})
	case "list":
		fs.BoolVar(&jsonOut, "json", jsonOut, "Emit JSON")
	case "show":
		positional = 1
		fs.BoolVar(&jsonOut, "json", jsonOut, "Emit JSON")
	case "remove":
		positional = 1
	default:
		fs.Usage()
		return 2
	}
	fs.StringVar(&dataDir, "data-dir", dataDir, "Runtime data directory")
	if err := fs.Parse(reorderArgs(args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != positional || (action == "add" && binding.WorkerID == "") {
		fs.Usage()
		return 2
	}

	path := authConfigPath(dataDir)
	explicitConfig := os.Getenv("MUXLIO_CONFIG") != ""
	var err error
	switch action {
	case "add":
		binding.Path = fs.Arg(1)
		err = addWorkspace(path, explicitConfig, fs.Arg(0), binding)
	case "remove":
		err = editWorkspaceConfig(path, explicitConfig, false, func(config *workspaceConfig) (workspaceEntry, error) {
			id, binding, err := config.registry.Resolve(fs.Arg(0))
			if err != nil {
				return workspaceEntry{}, err
			}
			delete(config.rawBindings, id)
			return workspaceEntry{id, binding}, nil
		})
	default:
		err = readWorkspaces(path, explicitConfig, action, fs.Arg(0), jsonOut)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

type workspaceConfig struct {
	fields      map[string]json.RawMessage
	registryKey string
	rawBindings map[string]json.RawMessage
	registry    workers.WorkspaceRegistry
}

func loadWorkspaceConfig(path string, explicit bool) (*workspaceConfig, error) {
	config := &workspaceConfig{
		fields:      make(map[string]json.RawMessage),
		registryKey: "workspaces",
		rawBindings: make(map[string]json.RawMessage),
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) && !explicit {
		// A dangling symlink still selects a config file.
		if _, linkErr := os.Lstat(path); os.IsNotExist(linkErr) {
			return config, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config %q must be a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := config.decode(raw, filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	return config, nil
}

func (c *workspaceConfig) decode(raw []byte, dir string) error {
	fields, err := decodeConfigObject(raw)
	if err != nil {
		return err
	}
	c.fields = fields
	foundRegistry := false
	for key, value := range fields {
		if !strings.EqualFold(key, "workspaces") {
			continue
		}
		if foundRegistry {
			return fmt.Errorf("multiple workspaces fields")
		}
		foundRegistry = true
		c.registryKey = key
		if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			c.rawBindings, err = decodeConfigObject(value)
			if err != nil {
				return fmt.Errorf("workspaces: %w", err)
			}
		}
	}
	var settings serviceConfig
	if err := decodeServiceConfig(raw, dir, &settings); err != nil {
		return err
	}
	if err := validateServiceConfig(settings); err != nil {
		return err
	}
	c.registry = settings.Workspaces
	return nil
}

// Retain unknown JSON values and reject duplicate keys before an edit can
// collapse them into one. Nested values outside the registry remain raw JSON.
func decodeConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON")
	}
	if token, _ := decoder.Token(); token != json.Delim('{') {
		return nil, fmt.Errorf("expected a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key := token.(string)
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate JSON key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	return fields, nil
}

func addWorkspace(path string, explicit bool, id string, binding workers.WorkspaceBinding) error {
	if err := api.ValidateWorkerID(binding.WorkerID); err != nil {
		return err
	}
	for _, name := range append([]string{id}, binding.Aliases...) {
		if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("invalid workspace identity %q", name)
		}
	}
	if err := (workers.WorkspaceRegistry{id: binding}).Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(binding.Path) || strings.ContainsRune(binding.Path, 0) {
		return fmt.Errorf("invalid workspace path %q", binding.Path)
	}
	if binding.WorkerID == "local" {
		canonical, err := workers.CanonicalWorkspace(binding.Path)
		if err != nil {
			return err
		}
		binding.Path = canonical
	}

	return editWorkspaceConfig(path, explicit, true, func(config *workspaceConfig) (workspaceEntry, error) {
		if _, exists := config.registry[id]; exists {
			return workspaceEntry{}, fmt.Errorf("workspace %q already exists", id)
		}
		if config.registry == nil {
			config.registry = make(workers.WorkspaceRegistry)
		}
		config.registry[id] = binding
		if err := config.registry.Validate(); err != nil {
			return workspaceEntry{}, err
		}
		for otherID, other := range config.registry {
			if otherID == id || other.WorkerID != binding.WorkerID {
				continue
			}
			otherPath := other.Path
			if binding.WorkerID == "local" {
				if canonical, err := workers.CanonicalWorkspace(other.Path); err == nil {
					otherPath = canonical
				}
			}
			if otherPath == binding.Path {
				return workspaceEntry{}, fmt.Errorf("workspaces %q and %q use the same directory on worker %q; configure aliases on one identity", otherID, id, binding.WorkerID)
			}
		}
		raw, err := json.Marshal(binding)
		if err != nil {
			return workspaceEntry{}, err
		}
		config.rawBindings[id] = raw
		return workspaceEntry{id, binding}, nil
	})
}

func editWorkspaceConfig(path string, explicit, adding bool, edit func(*workspaceConfig) (workspaceEntry, error)) error {
	if _, err := os.Lstat(path); err == nil {
		// Replace the selected config target, preserving a symlink to it.
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) || explicit {
		return err
	} else if adding {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
	}

	// Serialize workspace edits in this directory across atomic renames.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking config directory: %w", err)
	}

	config, err := loadWorkspaceConfig(path, explicit)
	if err != nil {
		return err
	}
	entry, err := edit(config)
	if err != nil {
		return err
	}
	rawBindings, err := json.Marshal(config.rawBindings)
	if err != nil {
		return err
	}
	config.fields[config.registryKey] = rawBindings
	raw, err := json.MarshalIndent(config.fields, "", "  ")
	if err != nil {
		return err
	}
	if err := writeRuntimeConfig(path, append(raw, '\n')); err != nil {
		return fmt.Errorf("writing config %q: %w", path, err)
	}
	action := "Added"
	if !adding {
		action = "Removed"
	}
	fmt.Printf("%s workspace %q in local config %q.\n", action, entry.ID, path)
	if adding && entry.WorkerID != "local" {
		fmt.Println("The owning worker validates its directory at startup.")
	}
	if adding {
		fmt.Println("If the owning worker uses a separate config, provision the same binding in both the coordinator and worker configs.")
	} else {
		fmt.Println("If the owning worker uses a separate config, remove the same binding from both the coordinator and worker configs.")
	}
	fmt.Printf("Restart the coordinator and owning worker %q to load the change.\n", entry.WorkerID)
	return nil
}

func readWorkspaces(path string, explicit bool, action, id string, jsonOut bool) error {
	config, err := loadWorkspaceConfig(path, explicit)
	if err != nil {
		return err
	}
	if action == "show" {
		id, binding, err := config.registry.Resolve(id)
		if err != nil {
			return err
		}
		entry := workspaceEntry{id, binding}
		if jsonOut {
			return json.NewEncoder(os.Stdout).Encode(entry)
		}
		printWorkspace(entry)
		return nil
	}
	ids := make([]string, 0, len(config.registry))
	for id := range config.registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]workspaceEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, workspaceEntry{id, config.registry[id]})
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Println("No workspaces configured.")
	}
	for _, entry := range entries {
		printWorkspace(entry)
	}
	return nil
}

func printWorkspace(entry workspaceEntry) {
	fmt.Printf("%q  worker=%q  path=%q", entry.ID, entry.WorkerID, entry.Path)
	if entry.Digest != "" {
		fmt.Printf("  digest=%s", entry.Digest)
	}
	if len(entry.Aliases) > 0 {
		fmt.Printf("  aliases=%q", entry.Aliases)
	}
	fmt.Println()
}
