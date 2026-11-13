// Package coordinatorconfig resolves explicitly selected coordinator credentials.
package coordinatorconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type coordinatorCredentials struct {
	Token     string `json:"token"`
	TokenFile string `json:"tokenFile"`
}

type coordinatorProfile struct {
	URL           string `json:"url"`
	CredentialRef string `json:"credentialRef"`
	coordinatorCredentials
}

func Resolve(target string) (string, string, error) {
	path := os.Getenv("MUXLIO_CONFIG")
	if path == "" {
		root := os.Getenv("MUXLIO_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", "", err
			}
			root = filepath.Join(home, ".muxlio")
		}
		path = filepath.Join(root, "config.json")
	}
	return ResolveFile(path, target, os.Getenv("MUXLIO_SERVER"), os.Getenv("MUXLIO_TOKEN"))
}

// ResolveFile keeps explicit configuration and environment selection identical for setup diagnostics and MCP.
func ResolveFile(path, target, envServer, envToken string) (string, string, error) {
	var config struct {
		DefaultServer string                     `json:"defaultServer"`
		Servers       map[string]json.RawMessage `json:"servers"`
		Profiles      map[string]json.RawMessage `json:"profiles"`
		coordinatorCredentials
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &config); err != nil {
			return "", "", fmt.Errorf("invalid coordinator config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading coordinator config: %w", err)
	}
	if target == "" {
		target = envServer
	}
	fromConfig := target == ""
	if fromConfig {
		target = config.DefaultServer
	}
	if target == "" {
		return "", "", fmt.Errorf("configure a coordinator with --server, MUXLIO_SERVER, or defaultServer")
	}
	selected := coordinatorProfile{URL: target}
	raw, named := config.Servers[target]
	if !named {
		raw, named = config.Profiles[target]
	}
	if strings.Contains(target, "://") && !fromConfig {
		named = false
	}
	if named {
		if json.Unmarshal(raw, &selected.URL) != nil {
			if err := json.Unmarshal(raw, &selected); err != nil {
				return "", "", fmt.Errorf("invalid server profile %q", target)
			}
		}
	}
	u, err := url.Parse(selected.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("unknown server profile or invalid coordinator URL; use a saved profile name or an HTTP(S) URL without credentials, query, or fragment")
	}
	readToken := func(creds coordinatorCredentials) (string, error) {
		if token := strings.TrimSpace(creds.Token); token != "" {
			return token, nil
		}
		if creds.TokenFile == "" {
			return "", nil
		}
		file := creds.TokenFile
		if !filepath.IsAbs(file) {
			file = filepath.Join(filepath.Dir(path), file)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading coordinator token file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if u.Scheme == "http" && !IsLoopback(u.Hostname()) {
		return "", "", fmt.Errorf("remote coordinator requires HTTPS; configure an explicit scoped profile")
	}
	if selected.CredentialRef != "" && selected.CredentialRef != "root" {
		return "", "", fmt.Errorf("unknown coordinator credentialRef; use root only for a local profile")
	}
	if selected.CredentialRef == "root" && !IsLoopback(u.Hostname()) {
		return "", "", fmt.Errorf("root credential reference requires a loopback coordinator; remote profiles require explicit scoped token or tokenFile")
	}
	if token := strings.TrimSpace(envToken); token != "" {
		return strings.TrimRight(selected.URL, "/"), token, nil
	}
	token, err := readToken(selected.coordinatorCredentials)
	if err != nil {
		return "", "", err
	}
	if token == "" && selected.CredentialRef == "root" {
		token, err = readToken(config.coordinatorCredentials)
	}
	return strings.TrimRight(selected.URL, "/"), token, err
}

// IsLoopback recognizes literal loopback endpoints without DNS inference.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
