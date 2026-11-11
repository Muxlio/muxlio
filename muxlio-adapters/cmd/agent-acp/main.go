// agent-acp runs any configured ACP v1 stdio agent through Muxlio result ports.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-adapters/acp"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

type params struct {
	MeteringAuthority string   `json:"meteringAuthority,omitempty"`
	Program           string   `json:"program"`
	Args              []string `json:"args"`
	Timeout           string   `json:"timeout"`
}

func defaultProgram() string {
	if program := os.Getenv("MUXLIO_ACP_PROGRAM"); program != "" {
		return program
	}
	return "acp-agent"
}

func main() {
	if adapter.ProbeRequested(os.Args) {
		// ACP has no standard version/login-status command. Do not invent one or
		// claim authentication was verified. Negotiation happens before prompting.
		adapter.RunProbe(adapter.BinaryCheck(defaultProgram()), contract.ProbeCheck{
			Name: contract.ProbeBackend, Passed: true,
			Message: "ACP stdio backend; protocol, capabilities and authentication checked at invocation",
		})
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-acp: %v", err)
	}
	result := execute(req)
	_ = adapter.WriteResponse(result)
}

func execute(req *adapter.Request) adapter.Response {
	fail := func(err error) adapter.Response {
		return adapter.Response{Outputs: map[string]any{}, Error: "agent-acp: " + err.Error()}
	}
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
	}
	if p.Program == "" {
		p.Program = defaultProgram()
	}
	if !req.HostExecutionAllowed {
		return fail(fmt.Errorf("host execution denied by worker policy"))
	}
	if err := adapter.CheckRunnerEnvironment(req, p.Program); err != nil {
		return fail(err)
	}
	cwd, err := adapter.WorkingDir(req)
	if err != nil {
		return fail(err)
	}
	for port, decl := range req.Outputs {
		name := decl.Path
		if name == "" {
			name = port
		}
		if err := adapter.ValidateOutputPath(req.WorkDir, name); err != nil {
			return fail(err)
		}
	}
	if p.Timeout == "" {
		p.Timeout = "10m"
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil || timeout <= 0 || timeout > 10*time.Minute {
		return fail(fmt.Errorf("timeout must be positive and at most 10m"))
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.Command(p.Program, p.Args...)
	cmd.Dir, cmd.Env = cwd, adapter.CleanEnvForChild()
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	defer stdout.Close()
	var stderr, text adapter.BoundedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	client := acp.New(stdout, stdin)
	meter := newACPAccounting(p.MeteringAuthority)
	client.SessionReady = func(id string) { meter.session = id }
	client.ModelSelected = func(id string) {
		_ = meter.recorder.ReportModel(contract.AccountingModel{ID: id, Role: "selected", Source: "acp-session-config"})
	}
	client.PromptStarted = func(id string) { meter.started = true }
	client.Negotiated = func(caps acp.Capabilities) {
		raw, _ := json.Marshal(caps)
		adapter.AppendLogChunk("acp", "ACP protocol 1; agent capabilities: "+string(raw)+"; permission requests, durable sessions and session loading unsupported")
	}
	client.Update = func(raw json.RawMessage) error {
		meter.observe(raw)
		var update struct {
			Kind    string          `json:"sessionUpdate"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &update); err != nil {
			return fmt.Errorf("invalid ACP update: %w", err)
		}
		switch update.Kind {
		case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
			var content struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			}
			if json.Unmarshal(update.Content, &content) != nil || content.Type == "" || (content.Type == "text" && content.Text == nil) {
				return fmt.Errorf("invalid ACP message chunk")
			}
			if update.Kind == "agent_message_chunk" && content.Type == "text" {
				adapter.CaptureAgentText(&text, *content.Text)
				return nil
			}
		}
		// Plans, tools, usage/context measurements and future updates are live
		// diagnostics only. They are never durable checkpoints or token estimates.
		adapter.AppendLogChunk("acp", string(raw))
		return nil
	}
	runErr := client.Execute(ctx, cwd, adapter.ComposePrompt(req))
	_ = stdin.Close()
	if runErr != nil {
		_ = cmd.Process.Kill()
	}
	// ACP agents normally live until EOF. Bound shutdown for agents that ignore it.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-time.After(time.Second):
		_ = cmd.Process.Kill()
		<-waited
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if runErr == nil {
		runErr = waitErr
	}
	finalAccounting := meter.finish()
	if runErr != nil {
		result := fail(runErr)
		result.Logs = stderr.String()
		result.Accounting = finalAccounting
		return result
	}
	outputs, missing, outputErrors := adapter.CollectOutputs(req)
	outputs["_text"] = map[string]any{"mime": "text/plain", "data": text.String()}
	result := adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Logs: stderr.String(), Accounting: finalAccounting}
	if len(missing) > 0 {
		result.Error = "agent-acp: missing expected output files: " + strings.Join(missing, ", ")
	}
	return result
}
