// Package integrations owns authoring-client setup. It is independent of the
// execution adapters, coordinator, workers and UI.
package integrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var Clients = []string{"claude", "codex", "cursor", "kiro"}

type Options struct {
	Client, Scope, Home, Workspace                    string
	Transport, Binary, Server, Config, URL, BearerEnv string
}

type Status struct {
	Client    string `json:"client"`
	Scope     string `json:"scope"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Managed   bool   `json:"managed"`
	Changed   bool   `json:"changed"`
}

func (o Options) Path() (string, error) {
	if o.Scope != "user" && o.Scope != "project" {
		return "", fmt.Errorf("select --scope user or --scope project explicitly")
	}
	base := o.Workspace
	if o.Scope == "user" {
		base = o.Home
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("scope directory must be absolute")
	}
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	switch o.Client {
	case "claude":
		if o.Scope == "user" {
			return filepath.Join(base, ".claude.json"), nil
		}
		return filepath.Join(base, ".mcp.json"), nil
	case "codex":
		if o.Scope == "user" && os.Getenv("CODEX_HOME") != "" {
			base = os.Getenv("CODEX_HOME")
			if !filepath.IsAbs(base) {
				return "", fmt.Errorf("CODEX_HOME must be absolute")
			}
			if resolved, err := filepath.EvalSymlinks(base); err == nil {
				base = resolved
			}
			return filepath.Join(base, "config.toml"), nil
		}
		return filepath.Join(base, ".codex", "config.toml"), nil
	case "cursor":
		return filepath.Join(base, ".cursor", "mcp.json"), nil
	case "kiro":
		return filepath.Join(base, ".kiro", "settings", "mcp.json"), nil
	case "antigravity":
		return "", fmt.Errorf("Antigravity MCP setup is unavailable until its installed CLI contract is verified; no settings changed")
	default:
		return "", fmt.Errorf("unknown client; choose claude, codex, cursor or kiro")
	}
}

func (o Options) entry() (map[string]any, error) {
	switch o.Transport {
	case "stdio":
		info, err := os.Stat(o.Binary)
		if !filepath.IsAbs(o.Binary) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, fmt.Errorf("--binary must name an absolute executable muxlio-mcp file")
		}
		if !filepath.IsAbs(o.Config) {
			return nil, fmt.Errorf("Muxlio config path must be absolute")
		}
		args := []any{}
		if o.Server != "" {
			// Only public endpoint URLs or simple saved profile names belong in
			// process arguments. Credentials stay in the protected runtime config.
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(o.Server) {
				u, err := url.Parse(o.Server)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return nil, fmt.Errorf("--server requires a saved profile name or HTTP(S) URL without credentials, query or fragment")
				}
			}
			args = append(args, "--server", o.Server)
		}
		return map[string]any{"command": o.Binary, "args": args, "env": map[string]any{"MUXLIO_CONFIG": o.Config}}, nil
	case "http":
		u, err := url.Parse(o.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("HTTP requires --url with an HTTP(S) endpoint without credentials, query or fragment")
		}
		if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
			return nil, fmt.Errorf("remote MCP endpoints require HTTPS")
		}
		entry := map[string]any{"url": o.URL}
		if o.Client == "claude" {
			entry["type"] = "http"
		}
		if o.BearerEnv != "" {
			if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(o.BearerEnv) {
				return nil, fmt.Errorf("--bearer-env must be an environment variable name")
			}
			switch o.Client {
			case "codex":
				entry["bearer_token_env_var"] = o.BearerEnv
			case "cursor":
				entry["headers"] = map[string]any{"Authorization": "Bearer ${env:" + o.BearerEnv + "}"}
			default:
				entry["headers"] = map[string]any{"Authorization": "Bearer ${" + o.BearerEnv + "}"}
			}
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("select --transport stdio or http")
	}
}

type document struct {
	root, servers map[string]any
	raw           []byte
	toml          bool
}

func read(path string, isTOML bool) (document, error) {
	d := document{root: map[string]any{}, toml: isTOML}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return d, fmt.Errorf("cannot read client config")
	}
	d.raw = raw
	if err == nil {
		if isTOML {
			err = toml.Unmarshal(raw, &d.root)
		} else {
			err = decodeJSON(raw, &d.root)
		}
		if err != nil || d.root == nil {
			return d, fmt.Errorf("invalid client config; fix its syntax before retrying (contents withheld)")
		}
		if _, err := json.Marshal(d.root); err != nil {
			return d, fmt.Errorf("client config contains values that cannot be safely merged")
		}
	}
	key := "mcpServers"
	if isTOML {
		key = "mcp_servers"
	}
	if value, exists := d.root[key]; exists {
		var ok bool
		d.servers, ok = value.(map[string]any)
		if !ok {
			return d, fmt.Errorf("%s must be an object/table; no settings changed", key)
		}
	} else {
		d.servers = map[string]any{}
		d.root[key] = d.servers
	}
	return d, nil
}

func decodeJSON(raw []byte, value any) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func checkWritePath(path string) error {
	// Refuse symlink targets so setup never redirects a write into another scope.
	for dir := path; ; dir = filepath.Dir(dir) {
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in client config path")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return nil
}

func atomicWrite(path string, raw []byte) error {
	if err := checkWritePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".muxlio-mcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Apply touches only the selected client/scope. The receipt contains a hash,
// never credentials, and prevents repair/removal of an entry edited by its owner.
func Apply(o Options, action string) (Status, error) {
	path, err := o.Path()
	s := Status{Client: o.Client, Scope: o.Scope, Path: path}
	if err != nil {
		return s, err
	}
	d, err := read(path, o.Client == "codex")
	if err != nil {
		return s, err
	}
	current, exists := d.servers["muxlio"]
	s.Installed = exists
	receipt := path + ".muxlio-receipt"
	owned, readErr := os.ReadFile(receipt)
	if readErr != nil && !os.IsNotExist(readErr) {
		return s, fmt.Errorf("cannot read integration receipt")
	}
	s.Managed = exists && strings.TrimSpace(string(owned)) == digest(current)
	if action == "doctor" && exists && s.Managed {
		entry, ok := current.(map[string]any)
		if !ok {
			return s, fmt.Errorf("invalid muxlio entry; use integrations repair")
		}
		installed := o
		installed.Binary, _ = entry["command"].(string)
		installed.URL, _ = entry["url"].(string)
		installed.Transport = "http"
		if installed.Binary != "" {
			installed.Transport = "stdio"
		}
		if _, err := installed.entry(); err != nil {
			return s, fmt.Errorf("registered endpoint or executable is invalid; use integrations repair with the desired connection options")
		}
	}
	if action == "list" || action == "doctor" {
		return s, nil
	}
	if action != "add" && action != "repair" && action != "remove" {
		return s, fmt.Errorf("unknown integration action")
	}
	if action == "remove" {
		if !exists {
			return s, nil
		}
		if !s.Managed {
			return s, fmt.Errorf("muxlio entry was not installed by Muxlio or has been edited; remove it manually from the selected config after review")
		}
		delete(d.servers, "muxlio")
	} else {
		entry, err := o.entry()
		if err != nil {
			return s, err
		}
		// Normalize TOML arrays and JSON arrays through JSON before comparing.
		if exists && digest(current) == digest(entry) {
			return s, nil
		}
		if exists && (action != "repair" || !s.Managed) {
			return s, fmt.Errorf("conflicting muxlio entry in selected config; preserve or rename the custom entry, or use repair for an unchanged Muxlio-managed entry")
		}
		d.servers["muxlio"] = entry
	}
	var raw []byte
	if d.toml {
		raw, err = toml.Marshal(d.root)
	} else {
		raw, err = json.MarshalIndent(d.root, "", "  ")
	}
	if err != nil {
		return s, fmt.Errorf("cannot encode client config")
	}
	// Validate serialization before replacing the file.
	var check map[string]any
	if d.toml {
		err = toml.Unmarshal(raw, &check)
	} else {
		err = decodeJSON(raw, &check)
	}
	if err != nil || digest(d.root) != digest(check) {
		return s, fmt.Errorf("client config cannot be safely round-tripped")
	}
	latest, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) || !bytes.Equal(latest, d.raw) {
		return s, fmt.Errorf("client config changed during setup; retry after the other writer finishes")
	}
	for _, target := range []string{path, receipt} {
		if err := checkWritePath(target); err != nil {
			return s, err
		}
	}
	if action != "remove" {
		if err := atomicWrite(receipt, []byte(digest(d.servers["muxlio"])+"\n")); err != nil {
			return s, err
		}
	}
	if err := atomicWrite(path, raw); err != nil {
		// Restore ownership if replacing the config fails after staging its
		// receipt. Otherwise the existing managed entry becomes unrepairable.
		if action != "remove" {
			var rollbackErr error
			if owned != nil {
				rollbackErr = atomicWrite(receipt, owned)
			} else {
				rollbackErr = os.Remove(receipt)
			}
			if rollbackErr != nil {
				return s, fmt.Errorf("config write failed and receipt restoration failed; inspect the selected config and receipt before retrying")
			}
		}
		return s, err
	}
	if action == "remove" {
		if err := os.Remove(receipt); err != nil && !os.IsNotExist(err) {
			return s, err
		}
	}
	s.Changed = true
	s.Installed = action != "remove"
	s.Managed = s.Installed
	return s, nil
}
