package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"

	"github.com/muxlio/muxlio-core/internal/coordinatorconfig"
)

// Existing installs migrate on repeat init. Defaults and owned profiles survive;
// only a literal default matching this coordinator gains an explicit credential reference.
func ensureLocalCoordinatorProfile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	addr := "127.0.0.1:7600"
	if raw := cfg["addr"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &addr); err != nil {
			return "", fmt.Errorf("addr must be host:port")
		}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", fmt.Errorf("addr must be host:port")
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	if !coordinatorconfig.IsLoopback(host) {
		return "", fmt.Errorf("local coordinator profile requires a loopback or wildcard addr; configure remote scoped profiles explicitly")
	}
	var cert string
	if raw := cfg["tlsCert"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &cert); err != nil {
			return "", fmt.Errorf("tlsCert must be a string")
		}
	}
	scheme := "http"
	if cert != "" {
		scheme = "https"
	}
	endpoint := scheme + "://" + net.JoinHostPort(host, port)
	servers := map[string]json.RawMessage{}
	if raw := cfg["servers"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
			return "", fmt.Errorf("servers must be an object")
		}
	}
	legacy := map[string]json.RawMessage{}
	if raw := cfg["profiles"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return "", fmt.Errorf("profiles must be an object")
		}
	}
	changed := false
	local := "local"
	// Never overwrite a user-owned name, including legacy profiles.
	for i := 1; ; i++ {
		raw, exists := servers[local]
		if !exists {
			raw, exists = legacy[local]
		}
		if !exists {
			servers[local], _ = json.Marshal(map[string]string{"url": endpoint, "credentialRef": "root"})
			changed = true
			break
		}
		var p struct{ URL, CredentialRef string }
		if json.Unmarshal(raw, &p) == nil && p.URL == endpoint && p.CredentialRef == "root" {
			break
		}
		local = fmt.Sprintf("local-%d", i)
	}
	var selected string
	if raw := cfg["defaultServer"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &selected); err != nil {
			return "", fmt.Errorf("defaultServer must be a string")
		}
	}
	registration := selected
	if selected == "" {
		selected = local
		registration = local
		cfg["defaultServer"], _ = json.Marshal(selected)
		changed = true
	} else if u, err := url.Parse(selected); err == nil && u.IsAbs() {
		// Preserve the effective selected profile, including its scoped credentials.
		// A URL-keyed legacy profile must not turn into a root-authenticated (or
		// credentialless) registration merely because arguments now use a name.
		profile, exists := servers[selected]
		if !exists {
			profile, exists = legacy[selected]
		}
		if !exists && strings.TrimRight(selected, "/") == endpoint {
			profile = servers[local]
			if profile == nil {
				profile = legacy[local]
			}
			servers[selected] = profile
			changed = true
		} else if !exists {
			profile, _ = json.Marshal(map[string]string{"url": selected})
		}
		registration = "default"
		var desired any
		if err := json.Unmarshal(profile, &desired); err != nil {
			return "", fmt.Errorf("invalid default server profile")
		}
		for i := 1; ; i++ {
			raw, exists := servers[registration]
			if !exists {
				raw, exists = legacy[registration]
			}
			if !exists {
				servers[registration] = profile
				changed = true
				break
			}
			var current any
			if json.Unmarshal(raw, &current) == nil && reflect.DeepEqual(current, desired) {
				break
			}
			registration = fmt.Sprintf("default-%d", i)
		}
	}
	if changed {
		cfg["servers"], _ = json.Marshal(servers)
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return "", err
		}
		if err := writeInitConfig(path, raw); err != nil {
			return "", err
		}
	}
	return registration, nil
}
