// Package mcp implements the Muxlio Model Context Protocol server.
//
// It speaks the MCP JSON-RPC 2.0 protocol over stdio or Streamable HTTP and exposes
// Muxlio operations as callable tools. Any MCP-capable assistant — Codex,
// Cursor, Claude Code, or another — can invoke these tools to build, check,
// run, and inspect Muxlio workflows.
//
// # Tools
//
//	check_plan      Validate a compiled Plan IR JSON document.
//	build           Compile a local workflow file.
//	test            Test a local workflow with fake executors.
//	submit_features Run the constructed feature workflow without pushing.
//	start_run       Submit and start a workflow run from a Plan IR document.
//	list_runs       List all runs on the coordinator.
//	get_run         Get the current state of a single run.
//	get_graph       Get the materialised execution graph for a run.
//	get_events      Retrieve paginated events for a run.
//	get_activations List the activation steps for a run.
//	get_attempts    Inspect execution attempts, timing, and usage.
//	get_logs        Retrieve captured worker logs for a run.
//	read_artifact   Read bounded artifact contents by digest.
//	get_artifact_metadata Retrieve artifact MIME type and size.
//	cancel_run      Cancel an active run.
//	pause_run       Pause a running run.
//	resume_run      Resume a paused run.
//	retry_run       Retry a failed run, preserving completed work.
//	send_signal     Deliver a typed signal to a waiting run.
//	get_workers     List live registered workers and active assignments.
//	server_health   Check coordinator liveness.
//
// # Usage
//
// By default the binary reads JSON-RPC messages from stdin and writes responses
// to stdout. Set --listen to serve Streamable HTTP at /mcp instead.
// Configure a coordinator with --server,
// MUXLIO_SERVER, or defaultServer, using a saved profile name or explicit URL.
//
//	muxlio-mcp [--server profile-or-URL]
//
// # MCP client configuration
//
// Add an entry to your IDE's MCP configuration pointing at this binary:
//
//	{
//	  "mcpServers": {
//	    "muxlio": {
//	      "command": "muxlio-mcp",
//	      "args": ["--server", "http://127.0.0.1:7600"]
//	    }
//	  }
//	}
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Version is the muxlio-mcp server version reported in the MCP initialize
// handshake. Commands set it before serving requests.
var Version = "0.2.0"

const mcpProtocolVersion = "2025-11-25"

// ---------------------------------------------------------------------------
// MCP JSON-RPC 2.0 types (minimal subset required for tool hosting)
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol object types.

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct{}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolDef struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

var tools = []toolDef{
	{
		Name: "check_plan",
		Description: "Validate a compiled Muxlio Plan IR JSON document. " +
			"Returns a list of validation errors (empty when the plan is valid).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{
					"description": "The Plan IR document as a JSON object.",
					"type":        "object",
				},
			},
			"required": []string{"plan"},
		},
	},
	{
		Name: "build",
		Description: "Compile a Muxlio workflow TypeScript file into a canonical Plan IR JSON document. " +
			"Returns the compiled plan with its digest and any validation errors. " +
			"The workflow source must be accessible from the filesystem path provided. " +
			"Use this before start_run when the agent has authored or modified a workflow file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"description": "Absolute or relative filesystem path to the .ts, .js, or .json workflow file.",
					"type":        "string",
				},
			},
			"required": []string{"path"},
		},
	},
	{
		Name: "test",
		Description: "Dry-run a Muxlio workflow locally using the fake executor and a fake-results configuration. " +
			"No coordinator or real adapters are required. " +
			"Returns the run outcome, events, and activation states. " +
			"Use this to verify workflow logic before deploying to a coordinator.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"description": "Absolute or relative filesystem path to the .ts, .js, or .json workflow file.",
					"type":        "string",
				},
				"input": map[string]any{
					"description": "Typed JSON input matching the plan's inputSchema. Use {} when the plan requires no input.",
					"type":        "object",
				},
				"fakeResults": map[string]any{
					"description": "Optional fake executor results configuration. " +
						"Supports byNode and byExecutor maps, each mapping identifiers to {outputs, error} objects. " +
						"Omit to use default fake (all nodes succeed with empty outputs).",
					"type": "object",
					"properties": map[string]any{
						"byNode": map[string]any{
							"type":        "object",
							"description": "Map of node ID → fake result.",
						},
						"byExecutor": map[string]any{
							"type":        "object",
							"description": "Map of executor name → fake result.",
						},
					},
				},
			},
			"required": []string{"path"},
		},
	},
	{
		Name: "get_artifact_metadata",
		Description: "Retrieve metadata for a stored artifact identified by its SHA-256 content digest. " +
			"Returns the digest, MIME type, and size in bytes. " +
			"Use this to inspect artifacts generated by a completed run before downloading them.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"digest": map[string]any{
					"description": "The artifact content digest, e.g. sha256:abcdef…",
					"type":        "string",
				},
			},
			"required": []string{"digest"},
		},
	},
	{
		Name: "start_run",
		Description: "Submit a compiled Plan IR to the coordinator and start a workflow run. " +
			"Returns the new run ID and initial state. " +
			"Uses the configured coordinator, which may run on another machine; no local service is required.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{
					"description": "The Plan IR document as a JSON object.",
					"type":        "object",
				},
				"input": map[string]any{
					"description": "Typed JSON input matching the plan's inputSchema. Use {} when the plan requires no input.",
					"type":        "object",
				},
			},
			"required": []string{"plan", "input"},
		},
	},
	{
		Name:        "list_runs",
		Description: "List all workflow runs known to the coordinator.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "get_run",
		Description: "Get the current state, output, and error of a single workflow run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID returned by start_run or list_runs.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "get_graph",
		Description: "Get the materialised execution graph (scopes and activations) for a run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name: "get_events",
		Description: "Retrieve paginated events for a run. " +
			"Use the returned nextAfter cursor to page through a long history.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID.",
					"type":        "string",
				},
				"after": map[string]any{
					"description": "Sequence cursor: return events with seq > after. Default 0 (all events).",
					"type":        "integer",
				},
				"limit": map[string]any{
					"description": "Maximum number of events to return. Default 50, max 200.",
					"type":        "integer",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "get_snapshots",
		Description: "Inspect immutable intermediate artifact snapshots, including failed or running attempts. These are not committed final outputs. Use read_artifact with a snapshot digest. Default returns history; current=true selects latest per output. Page with nextAfter.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"runId":        map[string]any{"type": "string"},
			"activationId": map[string]any{"type": "string"},
			"attemptId":    map[string]any{"type": "string"},
			"current":      map[string]any{"type": "boolean"},
			"after":        map[string]any{"type": "integer"},
			"limit":        map[string]any{"type": "integer"},
		}, "required": []string{"runId"}},
	},
	{
		Name:        "get_activations",
		Description: "List the materialised activation steps for a run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "get_logs",
		Description: "Retrieve captured worker log chunks for a run or a specific activation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID.",
					"type":        "string",
				},
				"activationId": map[string]any{
					"description": "Optional activation ID to filter logs to one step.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "cancel_run",
		Description: "Cancel an active run. Active worker steps are asked to stop; external effects already in flight may not be undoable.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID to cancel.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "pause_run",
		Description: "Pause scheduling for a run. The currently executing step is allowed to finish.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID to pause.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "resume_run",
		Description: "Resume a paused run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID to resume.",
					"type":        "string",
				},
			},
			"required": []string{"runId"},
		},
	},
	{
		Name:        "send_signal",
		Description: "Deliver a typed signal to a run that is waiting for it (a wait node with waitFor=signal).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"description": "The run ID.",
					"type":        "string",
				},
				"signalName": map[string]any{
					"description": "The signal name the run is waiting for.",
					"type":        "string",
				},
				"payload": map[string]any{
					"description": "Optional JSON payload for the signal. Use {} for a bare signal.",
					"type":        "object",
				},
				"idempotencyKey": map[string]any{"type": "string", "description": "Optional delivery key; reuse on retries to avoid duplicate consumption."},
			},
			"required": []string{"runId", "signalName"},
		},
	},
	{
		Name:        "get_input_requests",
		Description: "List durable input requests, including distinct request IDs, response schemas, permissions, deadlines and states.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"runId": map[string]any{"type": "string"}}, "required": []string{"runId"}},
	},
	{
		Name:        "respond_input_request",
		Description: "Respond to one durable input request. Reuse the idempotency key on retry: the accepted response is applied at most once. Required permissions and JSON Schema are enforced by the coordinator.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"runId": map[string]any{"type": "string"}, "requestId": map[string]any{"type": "string"}, "idempotencyKey": map[string]any{"type": "string"}, "response": map[string]any{}}, "required": []string{"runId", "requestId", "idempotencyKey", "response"}},
	},
	{
		Name:        "get_workers",
		Description: "List live registered workers (heartbeat window) and their active assignments.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "server_health",
		Description: "Check coordinator liveness. Returns {status: ok} when the coordinator is reachable.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

type apiClient struct {
	base   string
	token  string
	client *http.Client
}

func newAPIClient(baseURL string, tokens ...string) *apiClient {
	token := ""
	if len(tokens) > 0 {
		token = tokens[0]
	}
	return &apiClient{
		base:  strings.TrimRight(baseURL, "/"),
		token: strings.TrimSpace(token),
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type apiResponse struct {
	statusCode int
	body       []byte
}

func (c *apiClient) get(path string, query url.Values) (*apiResponse, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *apiClient) post(path string, body any, keys ...string) (*apiResponse, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if len(keys) > 0 {
		req.Header.Set("Idempotency-Key", keys[0])
	}
	return c.do(req)
}

func (c *apiClient) do(req *http.Request) (*apiResponse, error) {
	resp, err := c.open(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB limit
	if err != nil {
		return nil, err
	}
	return &apiResponse{statusCode: resp.StatusCode, body: body}, nil
}

// open applies coordinator authentication to both JSON and streaming requests.
func (c *apiClient) open(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.client.Do(req)
}

// asText returns a compact JSON representation of the API response suitable
// for returning as a tool result.
func (r *apiResponse) asText() string {
	// Pretty-print when body is JSON.
	var v any
	if json.Unmarshal(r.body, &v) == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(pretty)
		}
	}
	return string(r.body)
}

func (r *apiResponse) isError() bool {
	return r.statusCode < 200 || r.statusCode >= 300
}

// ---------------------------------------------------------------------------
// Plan IR validation (local, no coordinator required)
// ---------------------------------------------------------------------------

// validatePlanLocally runs the same Plan IR validator as the coordinator,
// without requiring a coordinator connection.
func validatePlanLocally(raw json.RawMessage) ([]map[string]any, error) {
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		d := diagnostic.New("MUXLIO_INVALID_JSON", "plan", "plan is not valid JSON: "+err.Error(), "Correct the JSON syntax and retry validation.")
		b, _ := diagnostic.JSON(d)
		var item map[string]any
		_ = json.Unmarshal(b, &item)
		return []map[string]any{item}, nil
	}

	validationErrors := plan.Validate(&p)
	errs := make([]map[string]any, 0, len(validationErrors))
	for _, validationErr := range validationErrors {
		encoded, _ := json.Marshal(validationErr)
		var item map[string]any
		_ = json.Unmarshal(encoded, &item)
		errs = append(errs, item)
	}
	return errs, nil
}

// ---------------------------------------------------------------------------
// Tool dispatch
// ---------------------------------------------------------------------------

type server struct {
	api *apiClient
}

func (s *server) dispatchTool(name string, args json.RawMessage) toolCallResult {
	switch name {
	case "submit_features":
		return s.toolSubmitFeatures(args)
	case "retry_run":
		return s.toolControlRun(args, "retry")
	case "get_attempts":
		return s.toolGetAttempts(args)
	case "read_artifact":
		return s.toolReadArtifact(args)
	case "check_plan":
		return s.toolCheckPlan(args)
	case "build":
		return s.toolBuild(args)
	case "test":
		return s.toolTest(args)
	case "get_artifact_metadata":
		return s.toolGetArtifactMetadata(args)
	case "start_run":
		return s.toolStartRun(args)
	case "list_runs":
		return s.toolListRuns()
	case "get_run":
		return s.toolGetRun(args)
	case "get_graph":
		return s.toolGetGraph(args)
	case "get_events":
		return s.toolGetEvents(args)
	case "get_snapshots":
		return s.toolGetSnapshots(args)
	case "get_activations":
		return s.toolGetActivations(args)
	case "get_logs":
		return s.toolGetLogs(args)
	case "cancel_run":
		return s.toolControlRun(args, "cancel")
	case "pause_run":
		return s.toolControlRun(args, "pause")
	case "resume_run":
		return s.toolControlRun(args, "resume")
	case "send_signal":
		return s.toolSendSignal(args)
	case "get_input_requests", "respond_input_request":
		return s.toolInputRequest(name, args)
	case "get_workers":
		return s.toolGetWorkers()
	case "server_health":
		return s.toolServerHealth()
	default:
		return errResult(fmt.Sprintf("unknown tool %q", name))
	}
}

// ---------------------------------------------------------------------------
// Individual tool implementations
// ---------------------------------------------------------------------------

func (s *server) toolCheckPlan(args json.RawMessage) toolCallResult {
	var params struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if len(params.Plan) == 0 {
		return errResult("plan is required")
	}

	errs, err := validatePlanLocally(params.Plan)
	if err != nil {
		return errResult("validation error: " + err.Error())
	}

	errs, warnings := splitValidationIssues(errs)
	result := map[string]any{
		"warnings": warnings,
		"valid":    len(errs) == 0,
		"errors":   errs,
	}

	// Also extract name/digest for convenience.
	var meta struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	_ = json.Unmarshal(params.Plan, &meta)
	if meta.Name != "" {
		result["name"] = meta.Name
	}
	if meta.Digest != "" {
		result["digest"] = meta.Digest
	}

	text, _ := diagnostic.JSON(result)
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: string(text)}},
		IsError: len(errs) > 0,
	}
}

// newFakeExecutorFromJSON builds a FakeExecutor from optional fakeResults JSON.
// The JSON format matches the --fake-config format used by the CLI.
func newFakeExecutorFromJSON(raw json.RawMessage) *workers.FakeExecutor {
	exec := workers.NewFake()
	if len(raw) == 0 {
		return exec
	}
	var cfg struct {
		ByNode map[string]struct {
			Outputs map[string]any `json:"outputs"`
			Error   string         `json:"error,omitempty"`
		} `json:"byNode"`
		ByExecutor map[string]struct {
			Outputs map[string]any `json:"outputs"`
			Error   string         `json:"error,omitempty"`
		} `json:"byExecutor"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return exec
	}
	for nodeID, r := range cfg.ByNode {
		exec.ByNode(nodeID, workers.ExecuteResult{Outputs: r.Outputs, Error: r.Error})
	}
	for executorName, r := range cfg.ByExecutor {
		exec.ByExecutor(executorName, workers.ExecuteResult{Outputs: r.Outputs, Error: r.Error})
	}
	return exec
}

func (s *server) toolBuild(args json.RawMessage) toolCallResult {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.Path == "" {
		return errResult("path is required")
	}

	absPath, err := filepath.Abs(params.Path)
	if err != nil {
		return errResult("invalid path: " + err.Error())
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	var rawPlan []byte

	switch ext {
	case ".json":
		rawPlan, err = os.ReadFile(absPath)
		if err != nil {
			return errResult("reading file: " + err.Error())
		}
	case ".ts", ".js", ".mjs":
		rawPlan, err = compilePlanFromFile(absPath)
		if err != nil {
			return errResult("compiling workflow: " + err.Error())
		}
	default:
		return errResult(fmt.Sprintf("unsupported file type %q (expected .ts, .js, or .json)", ext))
	}

	var p plan.Plan
	if err := json.Unmarshal(rawPlan, &p); err != nil {
		return errResult("parsing plan JSON: " + err.Error())
	}
	issues := plan.Validate(&p)
	verrs := plan.ErrorsOnly(issues)
	safeWarnings, _ := diagnostic.JSON(plan.WarningsOnly(issues))

	result := map[string]any{
		"warnings":  json.RawMessage(safeWarnings),
		"valid":     len(verrs) == 0,
		"name":      p.Name,
		"irVersion": p.IRVersion,
		"digest":    p.Digest,
		"subgraphs": len(p.Subgraphs),
	}
	if len(verrs) > 0 {
		errList := make([]map[string]any, 0, len(verrs))
		for _, ve := range verrs {
			item, _ := json.Marshal(ve)
			var m map[string]any
			_ = json.Unmarshal(item, &m)
			errList = append(errList, m)
		}
		result["errors"] = errList
	} else {
		// Include the compiled plan so start_run can use it directly.
		var planObj any
		_ = json.Unmarshal(rawPlan, &planObj)
		result["plan"] = planObj
	}

	var text []byte
	if len(verrs) > 0 {
		text, _ = diagnostic.JSON(result)
	} else {
		// The compiled plan is executable data; diagnostic bounds must not
		// silently drop nodes, bindings, or task content.
		text, _ = json.Marshal(result)
	}
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: string(text)}},
		IsError: len(verrs) > 0,
	}
}

// compilePlanFromFile invokes npx tsx (or node) to execute the TypeScript
// workflow file and extract the Plan IR, mirroring the logic in the CLI's
// build command.
func compilePlanFromFile(filePath string) ([]byte, error) {
	dir := filepath.Dir(filePath)
	// Try extracting the exported plan from the module.
	runnerSnippet := fmt.Sprintf(`
import { pathToFileURL } from 'node:url';
const target = %q;
import(pathToFileURL(target).href).then((mod) => {
  const def = mod.default || mod;
  const p = def?.plan;
  if (!p || !p.subgraphs) process.exit(1);
  process.stdout.write(JSON.stringify(p));
  process.exit(0);
}).catch(() => process.exit(1));
`, filePath)

	cmd := exec.Command("npx", "tsx", "-e", runnerSnippet)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil && stdout.Len() > 0 {
		return stdout.Bytes(), nil
	}

	return nil, fmt.Errorf("could not compile workflow %s (stderr: %s)", filePath, stderr.String())
}

func (s *server) toolTest(args json.RawMessage) toolCallResult {
	var params struct {
		Path        string          `json:"path"`
		Input       json.RawMessage `json:"input"`
		FakeResults json.RawMessage `json:"fakeResults"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.Path == "" {
		return errResult("path is required")
	}
	if len(params.Input) == 0 {
		params.Input = json.RawMessage(`{}`)
	}

	absPath, err := filepath.Abs(params.Path)
	if err != nil {
		return errResult("invalid path: " + err.Error())
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	var rawPlan []byte
	switch ext {
	case ".json":
		rawPlan, err = os.ReadFile(absPath)
		if err != nil {
			return errResult("reading file: " + err.Error())
		}
	case ".ts", ".js", ".mjs":
		rawPlan, err = compilePlanFromFile(absPath)
		if err != nil {
			return errResult("compiling workflow: " + err.Error())
		}
	default:
		return errResult(fmt.Sprintf("unsupported file type %q", ext))
	}

	var p plan.Plan
	if err := json.Unmarshal(rawPlan, &p); err != nil {
		return errResult("parsing plan: " + err.Error())
	}
	issues := plan.Validate(&p)
	if verrs := plan.ErrorsOnly(issues); len(verrs) > 0 {
		errList := make([]map[string]any, 0, len(verrs))
		for _, ve := range verrs {
			item, _ := json.Marshal(ve)
			var m map[string]any
			_ = json.Unmarshal(item, &m)
			errList = append(errList, m)
		}
		out, _ := diagnostic.JSON(map[string]any{"test": "failed", "errors": errList, "warnings": plan.WarningsOnly(issues)})
		return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(out)}}, IsError: true}
	}

	fakeExec := newFakeExecutorFromJSON(params.FakeResults)
	engine := plan.NewEngine(&p, fakeExec)

	runID, err := engine.CreateRun(params.Input)
	if err != nil {
		return errResult("creating run: " + err.Error())
	}
	runErr := engine.Run(runID)

	store := engine.Store()
	run, _ := store.GetRun(runID)
	events, _ := store.GetEvents(runID, 0, 0)
	activations, _ := store.ListActivations(runID)

	var stateStr, errMsg string
	var outputVal any
	if run != nil {
		stateStr = run.State
		outputVal = run.Output
		errMsg = run.Error
	}
	if runErr != nil && errMsg == "" {
		errMsg = runErr.Error()
	}

	evtList := make([]map[string]any, 0, len(events))
	for _, e := range events {
		item := map[string]any{"seq": e.Seq, "type": e.Type, "subjectId": e.SubjectID}
		evtList = append(evtList, item)
	}

	testStatus := "passed"
	if run == nil || run.State != "succeeded" {
		testStatus = "failed"
	}

	result := map[string]any{
		"diagnostics": state.AttemptDiagnostics(mustAttempts(store, runID)),
		"warnings":    plan.WarningsOnly(issues),
		"test":        testStatus,
		"workflow":    p.Name,
		"runId":       runID,
		"state":       stateStr,
		"output":      outputVal,
		"error":       errMsg,
		"activations": len(activations),
		"events":      evtList,
	}
	text, _ := diagnostic.JSON(result)
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: string(text)}},
		IsError: testStatus == "failed",
	}
}

func (s *server) toolGetArtifactMetadata(args json.RawMessage) toolCallResult {
	var params struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.Digest == "" {
		return errResult("digest is required")
	}

	// HEAD request to the artifact endpoint to retrieve metadata without
	// downloading the full content. Fall back to GET if HEAD is not allowed.
	headReq, err := http.NewRequest(http.MethodHead, s.api.base+"/v1/artifacts/"+url.PathEscape(params.Digest), nil)
	if err != nil {
		return errResult("creating request: " + err.Error())
	}
	headResp, err := s.api.open(headReq)
	if err == nil && (headResp.StatusCode == http.StatusMethodNotAllowed || headResp.StatusCode == http.StatusNotImplemented) {
		headResp.Body.Close()
		getReq, _ := http.NewRequest(http.MethodGet, headReq.URL.String(), nil)
		headResp, err = s.api.open(getReq)
	}
	if err == nil {
		defer headResp.Body.Close()
		if headResp.StatusCode == http.StatusOK {
			result := map[string]any{
				"digest":      headResp.Header.Get("X-Artifact-SHA256"),
				"size":        headResp.ContentLength,
				"contentType": headResp.Header.Get("Content-Type"),
			}
			if result["digest"] == "" {
				result["digest"] = params.Digest
			}
			text, _ := diagnostic.JSON(result)
			return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(text)}}}
		}
		body, _ := io.ReadAll(io.LimitReader(headResp.Body, 64<<10))
		return errResult(fmt.Sprintf("coordinator returned HTTP %d: %s", headResp.StatusCode, strings.TrimSpace(string(body))))
	}
	return errResult("coordinator request failed: " + err.Error())
}

func (s *server) toolStartRun(args json.RawMessage) toolCallResult {
	var params struct {
		Plan  json.RawMessage `json:"plan"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if len(params.Plan) == 0 {
		return errResult("plan is required")
	}
	if len(params.Input) == 0 {
		params.Input = json.RawMessage(`{}`)
	}

	// Local pre-flight validation before touching the coordinator.
	errs, _ := validatePlanLocally(params.Plan)
	errs, _ = splitValidationIssues(errs)
	if len(errs) > 0 {
		text, _ := json.MarshalIndent(map[string]any{
			"code":    "MUXLIO_PLAN_INVALID",
			"message": "plan failed local validation; fix errors before submitting",
			"errors":  errs,
		}, "", "  ")
		return toolCallResult{
			Content: []contentBlock{{Type: "text", Text: string(text)}},
			IsError: true,
		}
	}

	body := map[string]json.RawMessage{
		"plan":  params.Plan,
		"input": params.Input,
	}
	resp, err := s.api.post("/v1/runs", body)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolListRuns() toolCallResult {
	resp, err := s.api.get("/v1/runs", nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetRun(args json.RawMessage) toolCallResult {
	runID, err := extractRunID(args)
	if err != nil {
		return errResult(err.Error())
	}
	resp, err := s.api.get("/v1/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetGraph(args json.RawMessage) toolCallResult {
	runID, err := extractRunID(args)
	if err != nil {
		return errResult(err.Error())
	}
	resp, err := s.api.get("/v1/runs/"+url.PathEscape(runID)+"/graph", nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetEvents(args json.RawMessage) toolCallResult {
	var params struct {
		RunID string `json:"runId"`
		After *int   `json:"after"`
		Limit *int   `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.RunID == "" {
		return errResult("runId is required")
	}

	q := url.Values{}
	if params.After != nil {
		q.Set("after", strconv.Itoa(*params.After))
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
		if limit > 200 {
			limit = 200
		}
		if limit < 1 {
			limit = 1
		}
	}
	q.Set("limit", strconv.Itoa(limit))

	resp, err := s.api.get("/v1/runs/"+url.PathEscape(params.RunID)+"/events", q)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetActivations(args json.RawMessage) toolCallResult {
	runID, err := extractRunID(args)
	if err != nil {
		return errResult(err.Error())
	}
	resp, err := s.api.get("/v1/runs/"+url.PathEscape(runID)+"/activations", nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetLogs(args json.RawMessage) toolCallResult {
	var params struct {
		RunID        string `json:"runId"`
		ActivationID string `json:"activationId"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.RunID == "" {
		return errResult("runId is required")
	}

	path := "/v1/runs/" + url.PathEscape(params.RunID) + "/logs"
	var q url.Values
	if params.ActivationID != "" {
		q = url.Values{"activationId": {params.ActivationID}}
	}
	resp, err := s.api.get(path, q)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolControlRun(args json.RawMessage, action string) toolCallResult {
	runID, err := extractRunID(args)
	if err != nil {
		return errResult(err.Error())
	}
	resp, err := s.api.post("/v1/runs/"+url.PathEscape(runID)+"/"+action, map[string]any{})
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolSendSignal(args json.RawMessage) toolCallResult {
	var params struct {
		RunID          string          `json:"runId"`
		SignalName     string          `json:"signalName"`
		Payload        json.RawMessage `json:"payload"`
		IdempotencyKey string          `json:"idempotencyKey"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.RunID == "" {
		return errResult("runId is required")
	}
	if params.SignalName == "" {
		return errResult("signalName is required")
	}

	var payload any
	if len(params.Payload) > 0 {
		_ = json.Unmarshal(params.Payload, &payload)
	}

	resp, err := s.api.post("/v1/runs/"+url.PathEscape(params.RunID)+"/signals/"+url.PathEscape(params.SignalName), payload, params.IdempotencyKey)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolGetWorkers() toolCallResult {
	resp, err := s.api.get("/v1/workers", nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}

func (s *server) toolServerHealth() toolCallResult {
	resp, err := s.api.get("/healthz", nil)
	if err != nil {
		return errResult("coordinator unreachable: " + err.Error())
	}
	return apiResultOrError(resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func extractRunID(args json.RawMessage) (string, error) {
	var params struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", errors.New("invalid arguments: " + err.Error())
	}
	if params.RunID == "" {
		return "", errors.New("runId is required")
	}
	return params.RunID, nil
}

func errResult(msg string) toolCallResult {
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: diagnosticText(msg)}},
		IsError: true,
	}
}

func apiResultOrError(r *apiResponse) toolCallResult {
	text := r.asText()
	if r.isError() {
		return toolCallResult{
			Content: []contentBlock{{Type: "text", Text: safeErrorText(text, r.statusCode)}},
			IsError: true,
		}
	}
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: text}},
	}
}

// ---------------------------------------------------------------------------
// MCP protocol loop
// ---------------------------------------------------------------------------

type mcpServer struct {
	tools  *server
	enc    *json.Encoder
	logger *json.Encoder // writes to stderr for MCP debug logging
}

func (s *mcpServer) send(resp jsonRPCResponse) {
	_ = s.enc.Encode(resp)
}

func (s *mcpServer) handle(msg jsonRPCRequest) {
	switch msg.Method {
	case "initialize":
		s.send(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result: initializeResult{
				ProtocolVersion: mcpProtocolVersion,
				Capabilities:    capabilities{},
				ServerInfo:      serverInfo{Name: "muxlio", Version: Version},
				Instructions:    "Use submit_features to run the MCP-constructed implement-features workflow: implementation, review, verification/fix, and local commits, without pushing. Inspect get_workers first. Submission returns immediately; poll get_run and use get_activations, get_attempts, get_logs, and read_artifact to inspect progress and reports. Use retry_run only for a failed run after fixing its cause; completed work is preserved. build and test read local files and may execute workflow source code.",
			},
		})

	case "notifications/initialized":
		// No response needed for notifications.

	case "tools/list":
		s.send(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  toolsListResult{Tools: tools},
		})

	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.send(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Error:   &jsonRPCError{Code: -32602, Message: "invalid params: " + err.Error()},
			})
			return
		}
		result := s.tools.dispatchTool(params.Name, params.Arguments)
		s.send(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  result,
		})

	case "ping":
		s.send(jsonRPCResponse{JSONRPC: "2.0", ID: msg.ID, Result: map[string]any{}})

	default:
		if msg.ID != nil {
			// Only respond to requests (which have an ID), not notifications.
			s.send(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + msg.Method},
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// Main runs the standalone MCP command.
func Main() {
	serverURL := flag.String("server", "", "Muxlio coordinator URL or saved profile (default: $MUXLIO_SERVER or defaultServer)")
	listen := flag.String("listen", "", "serve Streamable HTTP at /mcp on this host:port instead of stdio")
	httpTokenFile := flag.String("http-token-file", "", "file containing the HTTP listener bearer token (default: $MUXLIO_MCP_HTTP_TOKEN or coordinator token)")
	tlsCert := flag.String("tls-cert", "", "HTTP listener TLS certificate PEM file")
	tlsKey := flag.String("tls-key", "", "HTTP listener TLS private key PEM file")
	var origins allowedOrigins
	flag.Var(&origins, "allow-origin", "allowed browser Origin for HTTP access (repeatable; default rejects browser origins)")
	flag.Parse()
	if *listen == "" && (*httpTokenFile != "" || *tlsCert != "" || *tlsKey != "" || len(origins) > 0) {
		fmt.Fprintln(os.Stderr, "muxlio-mcp: HTTP options require --listen")
		os.Exit(1)
	}

	base, token, err := resolveCoordinator(*serverURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "muxlio-mcp:", err)
		os.Exit(1)
	}
	api := &server{api: newAPIClient(base, token)}
	if *listen != "" {
		if err := runHTTP(api, *listen, *httpTokenFile, token, *tlsCert, *tlsKey, origins); err != nil {
			fmt.Fprintln(os.Stderr, "muxlio-mcp:", err)
			os.Exit(1)
		}
		return
	}
	srv := &mcpServer{
		tools: api,
		enc:   json.NewEncoder(os.Stdout),
		// MCP log messages go to stderr so they don't interfere with the protocol stream.
		logger: json.NewEncoder(os.Stderr),
	}

	// Log startup to stderr only.
	fmt.Fprintf(os.Stderr, "muxlio-mcp: coordinator=%s\n", base)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4<<20), 4<<20) // 4 MiB per line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg jsonRPCRequest
		if err := json.Unmarshal(line, &msg); err != nil {
			// Malformed message: emit a parse error response with null ID.
			srv.send(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &jsonRPCError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		msg.JSONRPC = "2.0"
		srv.handle(msg)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "muxlio-mcp: read error:", err)
		os.Exit(1)
	}
}

func diagnosticText(msg string) string {
	d := diagnostic.New("MUXLIO_TOOL_ERROR", "request", msg, "Correct the tool arguments or reported failure and retry.")
	b, _ := diagnostic.JSON(d)
	return string(b)
}
func safeErrorText(text string, status int) string {
	var v any
	if json.Unmarshal([]byte(text), &v) != nil {
		return diagnosticText(text)
	}
	if obj, ok := v.(map[string]any); ok {
		obj["httpStatus"] = status
	}
	b, _ := diagnostic.JSON(v)
	return string(b)
}

func mustAttempts(store state.Store, runID string) []state.ExecutionAttempt {
	attempts, _ := store.ListExecutionAttempts(runID, "")
	return attempts
}

func splitValidationIssues(issues []map[string]any) (errs, warnings []map[string]any) {
	for _, issue := range issues {
		if issue["severity"] == "warning" {
			warnings = append(warnings, issue)
		} else {
			errs = append(errs, issue)
		}
	}
	return
}
