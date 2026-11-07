package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// The CLI supports cached credentials as well as GEMINI_API_KEY. A readiness
// probe checks availability without decrypting credentials, logging account
// details, opening a login flow, or starting a generation. The CLI validates
// credentials when the actual task runs.
func geminiAuthenticationCheck() contract.ProbeCheck {
	check := contract.ProbeCheck{Name: contract.ProbeAuthentication, Message: "Configure GEMINI_API_KEY or sign in with gemini first"}
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) != "" {
		check.Passed, check.Message = true, "Gemini API key configured; validity checked during execution"
		return check
	}
	home := os.Getenv("GEMINI_CLI_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return check
	}
	dir := filepath.Join(home, ".gemini")
	var settings struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || json.Unmarshal(raw, &settings) != nil {
		return check
	}
	var name string
	switch settings.Security.Auth.SelectedType {
	case "gemini-api-key":
		name = "gemini-credentials.json"
	case "oauth-personal":
		name = "oauth_creds.json"
	default:
		return check
	}
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return check
	}
	defer file.Close()
	info, err := file.Stat()
	if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		check.Passed, check.Message = true, "Saved Gemini credentials available; validity checked during execution"
	}
	return check
}
