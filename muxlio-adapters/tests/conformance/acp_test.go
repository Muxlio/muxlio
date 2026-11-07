package conformance_test

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/conformancetest"
)

const acpInitialize = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":false}}}}`
const acpSession = `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"fixture-session"}}`
const acpUpdate = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ACP done."}}}}`
const acpComplete = `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func acpFixture() conformancetest.Fixture {
	return conformancetest.Fixture{
		Executor: "agent/acp", Binary: "agent-acp", CLIName: "acp-agent", UsageUnavailable: true,
		SuccessStream: strings.Join([]string{acpInitialize, acpSession, acpUpdate, acpComplete}, "\n"),
		MalformedStreams: []string{
			`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":99,"agentCapabilities":{}}}`,
			acpInitialize + "\n" + `{"jsonrpc":"2.0","id":2,"result":{}}`,
			acpInitialize + "\n" + acpSession + "\n" + `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"refusal"}}`,
			acpInitialize + "\n" + acpSession + "\n" + `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"wrong","update":{"sessionUpdate":"plan"}}}`,
			acpInitialize + "\n" + acpSession + "\n" + `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":12}}}}`,
		},
		Script: func(stream, stderr string, code int, scratch string) string {
			// Artifacts exist before the prompt response, just as for native adapters.
			script := "printf 'report' > " + shellQuote(filepath.Join(scratch, "report.txt")) + "\nprintf '{\"ok\":true}' > " + shellQuote(filepath.Join(scratch, "value")) + "\nprintf '%s\\n' " + shellQuote(stderr) + " >&2\n"
			for i, line := range strings.Split(stream, "\n") {
				if i < 3 {
					script += "IFS= read -r request || exit 0\n"
				}
				script += "printf '%s\\n' " + shellQuote(line) + "\n"
			}
			if code < 0 {
				return script + "kill -KILL $$\n"
			}
			return script + fmt.Sprintf("exit %d\n", code)
		},
		CancelReadyText: "ACP done.",
		CancelScript:    "#!/bin/sh\nIFS= read -r request\nprintf '%s\\n' " + shellQuote(acpInitialize) + "\nIFS= read -r request\nprintf '%s\\n' " + shellQuote(acpSession) + "\nIFS= read -r request\nprintf '%s\\n' " + shellQuote(acpUpdate) + "\nIFS= read -r request\ncase $request in *session/cancel*) printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"stopReason\":\"cancelled\"}}';; *) exit 2;; esac\n",
	}
}
