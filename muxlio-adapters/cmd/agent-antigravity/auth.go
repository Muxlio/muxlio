package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// The CLI supports cached credentials as well as ANTIGRAVITY_API_KEY.
// A readiness probe checks availability without decrypting credentials, logging
// account details, opening a login flow, or starting a generation. The CLI
// validates credentials when the actual task runs.
func antigravityAuthenticationCheck() contract.ProbeCheck {
	check := contract.ProbeCheck{Name: contract.ProbeAuthentication, Message: "Configure ANTIGRAVITY_API_KEY or sign in with agy first"}
	if strings.TrimSpace(os.Getenv("ANTIGRAVITY_API_KEY")) != "" {
		check.Passed, check.Message = true, "Antigravity API key configured; validity checked during execution"
		return check
	}
	home := os.Getenv("ANTIGRAVITY_CLI_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return check
	}
	// agy stores its OAuth token as a standalone file in its CLI state
	// directory; unlike API-key auth, it does not require a selectedType in
	// settings.json.
	oauthToken := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if info, err := os.Stat(oauthToken); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		check.Passed, check.Message = true, "Saved Antigravity login available; validity checked during execution"
		return check
	}

	dirs := []string{
		filepath.Join(home, ".antigravity"),
		filepath.Join(home, ".antigravity-cli"),
		filepath.Join(home, ".gemini", "antigravity-cli"),
	}

	for _, dir := range dirs {
		var settings struct {
			Security struct {
				Auth struct {
					SelectedType string `json:"selectedType"`
				} `json:"auth"`
			} `json:"security"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil || json.Unmarshal(raw, &settings) != nil {
			continue
		}
		var names []string
		switch settings.Security.Auth.SelectedType {
		case "antigravity-api-key", "api-key":
			names = []string{"antigravity-credentials.json", "credentials.json"}
		case "oauth-personal", "oauth":
			names = []string{"oauth_creds.json", "oauth-credentials.json"}
		default:
			continue
		}
		for _, name := range names {
			file, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			info, err := file.Stat()
			_ = file.Close()
			if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				check.Passed, check.Message = true, "Saved Antigravity credentials available; validity checked during execution"
				return check
			}
		}
	}
	return check
}
