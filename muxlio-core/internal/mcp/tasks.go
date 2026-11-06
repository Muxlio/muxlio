package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/muxlio/muxlio-core/internal/state"
)

const (
	tasksProtocolVersion = "2026-07-28"
	tasksExtension       = "io.modelcontextprotocol/tasks"
	taskHandlePrefix     = "mcp-run:"
)

type requestMeta struct {
	Version string
	Tasks   bool
	Form    bool
}

// Capabilities are request scoped, never inherited from initialize or another
// HTTP caller. In particular, the 2025 experimental tasks capability is ignored.
func requestMetadata(params json.RawMessage, transportVersion string) (requestMeta, *jsonRPCError) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(params, &p)
	var m requestMeta
	_ = json.Unmarshal(p.Meta["io.modelcontextprotocol/protocolVersion"], &m.Version)
	if m.Version != "" && m.Version != tasksProtocolVersion && m.Version != mcpProtocolVersion {
		return m, &jsonRPCError{Code: -32022, Message: "Unsupported protocol version", Data: map[string]any{"supported": []string{tasksProtocolVersion, mcpProtocolVersion}, "requested": m.Version}}
	}
	if transportVersion != "" && m.Version != "" && transportVersion != m.Version {
		return m, &jsonRPCError{Code: -32602, Message: "protocol version metadata and HTTP header disagree"}
	}
	if transportVersion == tasksProtocolVersion && m.Version == "" {
		return m, &jsonRPCError{Code: -32602, Message: "modern requests require protocol version metadata"}
	}
	if m.Version != tasksProtocolVersion {
		return m, nil
	}
	var info serverInfo
	var caps struct {
		Extensions  map[string]json.RawMessage `json:"extensions"`
		Elicitation map[string]json.RawMessage `json:"elicitation"`
	}
	if json.Unmarshal(p.Meta["io.modelcontextprotocol/clientInfo"], &info) != nil || info.Name == "" || info.Version == "" || json.Unmarshal(p.Meta["io.modelcontextprotocol/clientCapabilities"], &caps) != nil {
		return m, &jsonRPCError{Code: -32602, Message: "modern requests require clientInfo and clientCapabilities metadata"}
	}
	var settings map[string]any
	m.Tasks = json.Unmarshal(caps.Extensions[tasksExtension], &settings) == nil && settings != nil
	var form map[string]any
	m.Form = caps.Elicitation != nil && (len(caps.Elicitation) == 0 || (json.Unmarshal(caps.Elicitation["form"], &form) == nil && form != nil))
	return m, nil
}

func missingTaskCapability() *jsonRPCError {
	return &jsonRPCError{Code: -32021, Message: "Missing required client capability", Data: map[string]any{"requiredCapabilities": map[string]any{"extensions": map[string]any{tasksExtension: map[string]any{}}}}}
}

// The persisted run is the task record. This reversible, namespaced handle
// needs no process-local registry and cannot be mistaken for a plan task definition.
func taskHandle(runID string) string {
	return taskHandlePrefix + base64.RawURLEncoding.EncodeToString([]byte(runID))
}
func taskRunID(handle string) (string, bool) {
	if !strings.HasPrefix(handle, taskHandlePrefix) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, taskHandlePrefix))
	id := string(raw)
	return id, err == nil && id != "" && !strings.ContainsAny(id, "/\\.\x00") && taskHandle(id) == handle
}

func (s *server) taskForSubmission(result toolCallResult, meta requestMeta) map[string]any {
	if len(result.Content) != 1 {
		return nil
	}
	var submitted struct {
		RunID string `json:"runId"`
	}
	if json.Unmarshal([]byte(result.Content[0].Text), &submitted) != nil || submitted.RunID == "" {
		return nil
	}
	task, _, err := s.readTask(taskHandle(submitted.RunID), meta)
	// Preserve the successful ordinary result if read-back fails. Never invite a
	// retry of a committed submission or hand out an unresolvable task.
	if err != nil {
		return nil
	}
	task["resultType"] = "task"
	delete(task, "result")
	delete(task, "inputRequests")
	return task
}

func taskAPIError(resp *apiResponse, err error) *jsonRPCError {
	if err != nil {
		return &jsonRPCError{Code: -32603, Message: diagnosticText("coordinator request failed: " + err.Error())}
	}
	if resp.isError() {
		return &jsonRPCError{Code: -32000, Message: safeErrorText(resp.asText(), resp.statusCode)}
	}
	return nil
}

func (s *server) readTask(handle string, meta requestMeta) (map[string]any, []state.InputRequest, *jsonRPCError) {
	runID, ok := taskRunID(handle)
	if !ok {
		return nil, nil, &jsonRPCError{Code: -32602, Message: "invalid taskId"}
	}
	// Every read, input listing, update and cancellation goes through the normal
	// authenticated API. A handle is an identifier, never an authorization token.
	path := "/v1/runs/" + url.PathEscape(runID)
	resp, err := s.api.get(path, nil)
	if rpcErr := taskAPIError(resp, err); rpcErr != nil {
		return nil, nil, rpcErr
	}
	var run state.Run
	if json.Unmarshal(resp.body, &run) != nil || run.ID != runID || run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return nil, nil, &jsonRPCError{Code: -32603, Message: "invalid persisted run response"}
	}
	task := map[string]any{"resultType": "complete", "taskId": handle, "status": "working", "createdAt": run.CreatedAt, "lastUpdatedAt": run.UpdatedAt, "ttlMs": nil, "pollIntervalMs": 1000}
	switch run.State {
	case "succeeded", "failed":
		task["status"] = "completed"
		result := apiResultOrError(resp)
		result.IsError = run.State == "failed"
		task["result"] = map[string]any{"resultType": "complete", "content": result.Content, "isError": result.IsError}
		return task, nil, nil
	case "cancelled":
		task["status"] = "cancelled"
		return task, nil, nil
	}
	resp, err = s.api.get(path+"/input-requests", nil)
	if rpcErr := taskAPIError(resp, err); rpcErr != nil {
		return nil, nil, rpcErr
	}
	var listing struct {
		Requests []state.InputRequest `json:"requests"`
	}
	if json.Unmarshal(resp.body, &listing) != nil {
		return nil, nil, &jsonRPCError{Code: -32603, Message: "invalid input requests response"}
	}
	pending := []state.InputRequest{}
	requests := map[string]any{}
	for _, req := range listing.Requests {
		if req.State != state.InputRequestPending {
			continue
		}
		pending = append(pending, req)
		schema := req.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		// MCP forms only support flat primitive fields. JSON text preserves every
		// Muxlio schema, including nested objects, arrays, false and null.
		requests[req.RequestID] = map[string]any{"method": "elicitation/create", "params": map[string]any{"mode": "form", "message": req.Name, "requestedSchema": map[string]any{
			"type": "object", "properties": map[string]any{"json": map[string]any{"type": "string", "description": "JSON response matching schema: " + string(schema)}}, "required": []string{"json"},
		}}}
	}
	if len(pending) > 0 {
		if !meta.Form {
			return nil, nil, &jsonRPCError{Code: -32021, Message: "Outstanding input requires elicitation form capability", Data: map[string]any{"requiredCapabilities": map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}}}
		}
		task["status"] = "input_required"
		task["inputRequests"] = requests
	}
	return task, pending, nil
}

func (s *server) handleTask(method string, raw json.RawMessage, meta requestMeta) (any, *jsonRPCError) {
	if meta.Version != tasksProtocolVersion || !meta.Tasks {
		return nil, missingTaskCapability()
	}
	var p struct {
		TaskID         string                     `json:"taskId"`
		InputResponses map[string]json.RawMessage `json:"inputResponses"`
	}
	if json.Unmarshal(raw, &p) != nil || p.TaskID == "" || (method == "tasks/update" && p.InputResponses == nil) {
		return nil, &jsonRPCError{Code: -32602, Message: "taskId and, for update, inputResponses are required"}
	}
	readMeta := meta
	if method == "tasks/cancel" {
		// Cancellation does not deliver elicitation requests to the client.
		readMeta.Form = true
	}
	task, pending, rpcErr := s.readTask(p.TaskID, readMeta)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if method == "tasks/get" {
		return task, nil
	}
	runID, _ := taskRunID(p.TaskID)
	path := "/v1/runs/" + url.PathEscape(runID)
	if method == "tasks/cancel" {
		if task["status"] != "completed" && task["status"] != "cancelled" {
			resp, err := s.api.post(path+"/cancel", nil)
			if rpcErr := taskAPIError(resp, err); rpcErr != nil {
				return nil, rpcErr
			}
		}
	} else {
		for _, req := range pending {
			response, exists := p.InputResponses[req.RequestID]
			if !exists {
				continue
			}
			var input struct {
				Action  string                     `json:"action"`
				Content map[string]json.RawMessage `json:"content"`
			}
			if json.Unmarshal(response, &input) != nil || (input.Action != "accept" && input.Action != "decline" && input.Action != "cancel") {
				return nil, &jsonRPCError{Code: -32602, Message: "invalid elicitation response"}
			}
			if input.Action != "accept" {
				continue
			}
			var text string
			if json.Unmarshal(input.Content["json"], &text) != nil || !json.Valid([]byte(text)) {
				return nil, &jsonRPCError{Code: -32602, Message: "accepted response requires content.json containing valid JSON text"}
			}
			value := json.RawMessage(text)
			key := fmt.Sprintf("mcp-task:%x", sha256.Sum256(append([]byte(req.RequestID+":"), value...)))
			resp, err := s.api.post(path+"/input-requests/"+url.PathEscape(req.RequestID), value, key)
			if rpcErr := taskAPIError(resp, err); rpcErr != nil {
				return nil, rpcErr
			}
		}
	}
	return map[string]any{"resultType": "complete"}, nil
}
