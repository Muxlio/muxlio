package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-core/internal/api"
)

// Scoped credentials are loaded from protected files, never from workflow data.
func readAccessGrants(configPath string) ([]api.AccessGrant, error) {
	raw, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		AccessGrants []api.AccessGrant `json:"accessGrants"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return nil, fmt.Errorf("invalid access grant configuration")
	}
	seen := make(map[string]bool)
	for i := range config.AccessGrants {
		g := &config.AccessGrants[i]
		if g.TokenFile == "" {
			return nil, fmt.Errorf("access grant requires tokenFile")
		}
		path := g.TokenFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(configPath), path)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read access grant credential file")
		}
		g.Token = strings.TrimSpace(string(value))
		if g.Token == "" || seen[g.Token] {
			return nil, fmt.Errorf("access grant credentials must be nonempty and unique")
		}
		seen[g.Token] = true
	}
	return config.AccessGrants, nil
}

func readConfiguredAuthToken(configPath string) string {
	return readConfiguredToken(configPath, "token", "tokenFile")
}

func readConfiguredWorkerToken(configPath string) string {
	return readConfiguredToken(configPath, "workerToken", "workerTokenFile")
}

func readConfiguredToken(configPath, tokenKey, tokenFileKey string) string {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		return ""
	}
	var token string
	_ = json.Unmarshal(config[tokenKey], &token)
	if strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	var tokenFile string
	_ = json.Unmarshal(config[tokenFileKey], &tokenFile)
	if tokenFile == "" {
		return ""
	}
	if !filepath.IsAbs(tokenFile) {
		tokenFile = filepath.Join(filepath.Dir(configPath), tokenFile)
	}
	contents, err := os.ReadFile(tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(contents))
}

func authConfigPath(dataDir string) string {
	if path := os.Getenv("MUXLIO_CONFIG"); path != "" {
		return path
	}
	root := dataDir
	if root == "" {
		root = defaultRuntime()
	}
	return filepath.Join(root, "config.json")
}

// ensureAuthToken creates a persistent coordinator token on first setup and
// leaves existing configuration untouched on later starts.
func ensureAuthToken(configPath string) (string, error) {
	config := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(raw, &config); err != nil {
			return "", fmt.Errorf("reading config %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var token string
	if raw := config["token"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &token)
	}
	if strings.TrimSpace(token) != "" {
		_ = os.Chmod(filepath.Dir(configPath), 0o700)
		_ = os.Chmod(configPath, 0o600)
		return strings.TrimSpace(token), nil
	}
	var tokenFile string
	if raw := config["tokenFile"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &tokenFile)
	}
	if tokenFile != "" {
		if !filepath.IsAbs(tokenFile) {
			tokenFile = filepath.Join(filepath.Dir(configPath), tokenFile)
		}
		if raw, err := os.ReadFile(tokenFile); err == nil && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw)), nil
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generating coordinator token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(secret)
	encoded, _ := json.Marshal(token)
	config["token"] = encoded
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Dir(configPath), 0o700)
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return token, nil
}

// ensureWorkerToken persists a worker credential separate from the client token.
func ensureWorkerToken(configPath string) (string, error) {
	config := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(raw, &config); err != nil {
			return "", fmt.Errorf("reading config %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if token := readConfiguredWorkerToken(configPath); token != "" {
		return token, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generating worker token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	encoded, _ := json.Marshal(token)
	config["workerToken"] = encoded
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return "", err
	}
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return token, nil
}
