package integrations

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

const healthPrompt = "Verify the configured muxlio MCP server. Call its server_health tool and its get_workers tool once each with empty arguments. Do not use any other tools or change files. Do not simulate tool calls."

// VerifyClient runs the actual authoring client in the selected workspace, using
// its normal config loading, trust and authentication. No approval is bypassed.
// Starting a session can consume the client's model quota. Client output is
// deliberately withheld: tool responses and startup diagnostics can hold secrets.
func VerifyClient(ctx context.Context, o Options) error {
	if _, err := o.Path(); err != nil {
		return err
	}
	// Claude's private local-scope entries precede both project and user
	// registrations, despite being stored in the user config file.
	if o.Client == "claude" {
		user := o
		user.Scope = "user"
		path, err := user.Path()
		if err != nil {
			return err
		}
		d, err := read(path, false)
		if err != nil {
			return err
		}
		projects, _ := d.root["projects"].(map[string]any)
		workspace := o.Workspace
		if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
			workspace = resolved
		}
		for _, key := range []string{o.Workspace, workspace} {
			project, _ := projects[key].(map[string]any)
			servers, _ := project["mcpServers"].(map[string]any)
			if _, exists := servers["muxlio"]; exists {
				return fmt.Errorf("Claude local-scope muxlio entry overrides the selected registration; review that workspace entry in the Claude user config before retrying doctor")
			}
		}
	}
	// A project entry can override the selected user registration. Refuse to
	// certify a different server just because it has the same tool names.
	if o.Scope == "user" {
		project := o
		project.Scope = "project"
		path, err := project.Path()
		if err != nil {
			return err
		}
		d, err := read(path, o.Client == "codex")
		if err != nil {
			return err
		}
		if _, exists := d.servers["muxlio"]; exists {
			return fmt.Errorf("project muxlio entry may override user scope; run doctor with --scope project or select a workspace without a project muxlio entry")
		}
	}
	var binary string
	var args []string
	switch o.Client {
	case "claude":
		binary = "claude"
		args = []string{"-p", healthPrompt, "--output-format", "stream-json", "--verbose", "--max-turns", "4", "--allowedTools", "mcp__muxlio__server_health,mcp__muxlio__get_workers"}
	case "codex":
		binary = "codex"
		args = []string{"exec", "--json", "--ephemeral", "--sandbox", "read-only", healthPrompt}
	case "cursor":
		binary = "cursor"
		args = []string{"agent", "--print", "--output-format", "stream-json", healthPrompt}
	case "kiro":
		binary = "kiro-cli"
		args = []string{"chat", "--no-interactive", "--output-format", "stream-json", "--trust-tools", "muxlio___server_health,muxlio___get_workers", healthPrompt}
	default:
		return fmt.Errorf("client verification is unsupported")
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("%s CLI unavailable; open the client in this workspace and invoke muxlio server_health and get_workers; config-only checks use --config-only", binary)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = o.Workspace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cannot capture client verification")
	}
	// A nil Stderr connects directly to the null device. Using io.Discard
	// creates a copy goroutine that inherited child descriptors can keep alive
	// after cancellation, delaying doctor beyond its deadline.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start %s verification", binary)
	}
	// A client's subprocess can inherit stdout and keep it open after the
	// client is killed. Closing our reader also bounds that case by ctx.
	stop := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stop()
	verified, parseErr := toolEvidence(stdout, o.Client)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if err != nil || parseErr != nil || !verified {
		return fmt.Errorf("%s did not confirm successful muxlio server_health and get_workers calls; check client login, workspace trust, MCP approval, endpoint and service; retry doctor or invoke both tools in the client", binary)
	}
	return nil
}

func toolName(name string) string {
	for _, tool := range []string{"server_health", "get_workers"} {
		if name == "mcp__muxlio__"+tool || name == "muxlio___"+tool {
			return tool
		}
	}
	return ""
}

// Clients may embed a CallToolResult directly, in a text content block, or as
// JSON in rawOutput. Inspect decoded values rather than escaped JSON substrings.
func hasToolError(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if v["isError"] == true || v["is_error"] == true {
			return true
		}
		for _, child := range v {
			if hasToolError(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasToolError(child) {
				return true
			}
		}
	case string:
		// Only containers can contain an MCP error flag. Avoid recursively
		// decoding arbitrary strings (including nested JSON string literals).
		text := strings.TrimSpace(v)
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var decoded any
			if json.Unmarshal([]byte(text), &decoded) == nil {
				return hasToolError(decoded)
			}
		}
	}
	return false
}

// Accept structured tool events only, never a model's natural-language claim.
func toolEvidence(r io.Reader, client string) (bool, error) {
	seen := map[string]bool{}
	pending := map[string]string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if client == "codex" && event["type"] == "item.completed" {
			item, _ := event["item"].(map[string]any)
			result, _ := item["result"].(map[string]any)
			tool, _ := item["tool"].(string)
			if item["type"] == "mcp_tool_call" && item["server"] == "muxlio" && item["status"] == "completed" && item["error"] == nil && result != nil && !hasToolError(result) && (tool == "server_health" || tool == "get_workers") {
				seen[tool] = true
			}
		}
		if client == "claude" || client == "cursor" {
			message, _ := event["message"].(map[string]any)
			blocks, _ := message["content"].([]any)
			for _, value := range blocks {
				block, _ := value.(map[string]any)
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if block["type"] == "tool_use" && id != "" && toolName(name) != "" {
					pending[id] = toolName(name)
				}
				id, _ = block["tool_use_id"].(string)
				if block["type"] == "tool_result" && pending[id] != "" && !hasToolError(block) {
					seen[pending[id]] = true
				}
			}
		}
		if client == "cursor" && event["type"] == "tool_call" && event["subtype"] == "completed" {
			call, _ := event["tool_call"].(map[string]any)
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			result, _ := call["result"].(map[string]any)
			success, _ := result["success"].(map[string]any)
			if toolName(name) != "" && success != nil && !hasToolError(result) {
				seen[toolName(name)] = true
			}
			mcpCall, _ := call["mcpToolCall"].(map[string]any)
			args, _ := mcpCall["args"].(map[string]any)
			tool, _ := args["toolName"].(string)
			mcpResult, _ := mcpCall["result"].(map[string]any)
			mcpSuccess, _ := mcpResult["success"].(map[string]any)
			if args["providerIdentifier"] == "muxlio" && (tool == "server_health" || tool == "get_workers") && mcpSuccess != nil && !hasToolError(mcpResult) {
				seen[tool] = true
			}
		}
		if client == "kiro" {
			// Kiro's stream-json exposes ACP session/update envelopes.
			params, _ := event["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			if event["type"] == "sessionUpdate" {
				data, _ := event["data"].(map[string]any)
				update, _ = data["update"].(map[string]any)
				if update == nil {
					update = data
				}
			}
			if update == nil {
				update = event
			}
			id, _ := update["toolCallId"].(string)
			title, _ := update["title"].(string)
			if update["sessionUpdate"] == "tool_call" && id != "" && toolName(title) != "" {
				pending[id] = toolName(title)
			}
			if update["sessionUpdate"] == "tool_call_update" && update["status"] == "completed" && pending[id] != "" {
				if !hasToolError(update) {
					seen[pending[id]] = true
				}
			}
		}
	}
	return seen["server_health"] && seen["get_workers"], scanner.Err()
}
