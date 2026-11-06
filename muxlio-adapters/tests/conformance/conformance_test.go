package conformance_test

import (
	"github.com/muxlio/muxlio-adapters/conformancetest"
	"os"
	"path/filepath"
	"testing"
)

var bundledAdapters = []conformancetest.Fixture{
	{
		Executor: "agent/claude",
		Binary:   "agent-claude",
		CLIName:  "claude",
		// stream-json: result event with usage + text; Claude needs a result event to complete.
		SuccessStream: `{"type":"result","usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":40},"result":"All done."}`,
	},
	{
		Executor: "agent/codex",
		Binary:   "agent-codex",
		CLIName:  "codex",
		// JSONL: turn.completed with usage; item.completed with agent_message text.
		SuccessStream: `{"type":"item.completed","item":{"type":"agent_message","text":"Codex completed."}}
{"type":"turn.completed","usage":{"input_tokens":120,"output_tokens":30,"cached_input_tokens":40}}`,
	},
	{
		Executor: "agent/cursor",
		Binary:   "agent-cursor",
		CLIName:  "agent",
		// stream-json: result event with usage.
		SuccessStream: `{"type":"result","usage":{"inputTokens":120,"outputTokens":30,"cacheReadTokens":40},"result":"Cursor done."}`,
	},
	{
		Executor: "agent/gemini-cli",
		Binary:   "agent-gemini-cli",
		CLIName:  "gemini",
		// stream-json: result event with stats (documented headless format).
		SuccessStream: `{"type":"result","status":"success","stats":{"total_tokens":150,"input_tokens":120,"output_tokens":30,"cached":40,"input":80,"duration_ms":200,"tool_calls":0},"result":"Gemini done."}`,
	},
	{
		Executor: "agent/kiro",
		Binary:   "agent-kiro",
		CLIName:  "kiro-cli",
		MalformedStreams: []string{
			`{"type":"runFinished"}`,
			`{"type":"runFinished","data":null}`,
			`{"type":"runFinished","data":[]}`,
			`{"type":"runFinished","data":{"exitCode":"failed","status":"success"}}`,
		},
		// ACP stream-json: sessionUpdate with usage, then runFinished.
		// Usage is carried in the ACP metadata container under tokenUsage.
		SuccessStream: `{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","tokenUsage":{"input_tokens":120,"output_tokens":30,"cached_input_tokens":40}}}}}}
{"type":"runFinished","data":{"status":"success","finalText":"Kiro done."}}`,
	},
	{Executor: "command", Binary: "command", CLIName: "fixture-command", SuccessStream: "command output", RawOutput: true},
}

func TestBundledConformance(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, f := range bundledAdapters {
		covered[f.Binary] = true
	}
	for _, entry := range entries {
		if entry.IsDir() && !covered[entry.Name()] {
			t.Errorf("bundled adapter %s has no conformance fixture", entry.Name())
		}
	}
	conformancetest.Run(t, root, bundledAdapters)
}
