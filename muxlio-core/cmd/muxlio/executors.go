package main

import "strings"

// BundledExecutorCapabilities is the default coordinator capability set shipped
// with muxlio. It must stay aligned with adapter binaries under
// libexec/muxlio/adapters (see bundledAdapterBasenames).
var BundledExecutorCapabilities = []string{
	"agent/claude",
	"agent/codex",
	"agent/cursor",
	"agent/gemini-cli",
	"agent/kiro",
	"command",
}

func bundledAdapterBasenames() []string {
	out := make([]string, len(BundledExecutorCapabilities))
	for i, cap := range BundledExecutorCapabilities {
		out[i] = strings.ReplaceAll(cap, "/", "-")
	}
	return out
}
