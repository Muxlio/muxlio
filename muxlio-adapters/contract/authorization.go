package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// AuthorizationPolicy is supplied exclusively by the execution host operator.
// Task text, inputs, params and submitting-client privileges cannot grant it.
// Network covers agent tool traffic, not the runner's model API transport.
type AuthorizationPolicy struct {
	Mode            string   `json:"mode"`
	AllowedTools    []string `json:"allowedTools,omitempty"`
	AllowedCommands []string `json:"allowedCommands,omitempty"`
	WorkspaceWrite  bool     `json:"workspaceWrite"`
	Network         string   `json:"network"`
	Isolation       string   `json:"isolation"`
}

// Reject misspelled operator fields rather than silently ignoring restrictions.
func (p *AuthorizationPolicy) UnmarshalJSON(raw []byte) error {
	type plain AuthorizationPolicy
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("invalid execution authorization policy")
	}
	*p = AuthorizationPolicy(decoded)
	return p.Validate()
}

func (p *AuthorizationPolicy) Validate() error {
	if p == nil {
		// Missing policy is denied by native agent adapters.
		return nil
	}
	switch p.Mode {
	case "restricted", "trusted", "compatibility":
	default:
		return fmt.Errorf("unknown execution authorization mode")
	}
	if p.Network != "none" && p.Network != "allowed" {
		return fmt.Errorf("authorization network must be none or allowed")
	}
	if p.Isolation != "native" && p.Isolation != "required" {
		return fmt.Errorf("authorization isolation must be native or required")
	}
	for _, tool := range p.AllowedTools {
		switch tool {
		case "read-file", "write-file", "shell", "network", "*":
		default:
			return fmt.Errorf("unknown authorized tool")
		}
	}
	for _, command := range p.AllowedCommands {
		if command == "" {
			return fmt.Errorf("empty authorized command")
		}
	}
	if p.Mode != "restricted" && (!p.WorkspaceWrite || p.Network != "allowed" || len(p.AllowedTools) != 1 || p.AllowedTools[0] != "*" || len(p.AllowedCommands) != 1 || p.AllowedCommands[0] != "*") {
		return fmt.Errorf("trusted and compatibility modes require explicit unrestricted tools, commands, writes and network")
	}
	return nil
}

// Summary contains no commands, paths, environment values or other operator text.
func (p *AuthorizationPolicy) Summary() string {
	raw, _ := json.Marshal(p)
	digest := sha256.Sum256(raw)
	mode := "deny"
	if p != nil {
		mode = p.Mode
	}
	summary := "execution-authorization mode=" + mode + " digest=sha256:" + hex.EncodeToString(digest[:])
	if p != nil {
		summary += fmt.Sprintf(" tools=%d commands=%d workspaceWrite=%t network=%s isolation=%s", len(p.AllowedTools), len(p.AllowedCommands), p.WorkspaceWrite, p.Network, p.Isolation)
	}
	return summary
}
