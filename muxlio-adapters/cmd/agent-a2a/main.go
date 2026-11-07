// agent-a2a executes remote agents using A2A 0.3 JSON-RPC. Remote states
// stay here; the rest of Muxlio sees only pending references and final results.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

//go:embed adapter.json
var manifest []byte

type params struct {
	URL     string `json:"url"`
	CardURL string `json:"cardUrl"`
	Stream  bool   `json:"stream"`
	Timeout string `json:"timeout"`
}
type card struct {
	ProtocolVersion      string `json:"protocolVersion"`
	URL                  string `json:"url"`
	PreferredTransport   string `json:"preferredTransport"`
	AdditionalInterfaces []struct {
		URL       string `json:"url"`
		Transport string `json:"transport"`
	} `json:"additionalInterfaces"`
	DefaultInputModes []string `json:"defaultInputModes"`
	Capabilities      struct {
		Streaming  bool `json:"streaming"`
		Extensions []struct {
			URI      string `json:"uri"`
			Required bool   `json:"required"`
		} `json:"extensions"`
	} `json:"capabilities"`
	Security        []map[string][]string `json:"security"`
	SecuritySchemes map[string]struct {
		Type   string `json:"type"`
		Scheme string `json:"scheme"`
	} `json:"securitySchemes"`
}
type part struct {
	Kind string          `json:"kind"`
	Text string          `json:"text"`
	Data json.RawMessage `json:"data"`
	File *struct {
		Name     string  `json:"name"`
		MimeType string  `json:"mimeType"`
		Bytes    *string `json:"bytes"`
		URI      string  `json:"uri"`
	} `json:"file"`
}
type message struct {
	Parts []part `json:"parts"`
}
type task struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	Status    struct {
		State   string   `json:"state"`
		Message *message `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Name  string `json:"name"`
		Parts []part `json:"parts"`
	} `json:"artifacts"`
	Parts []part `json:"parts"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type client struct {
	http            *http.Client
	endpoint, token string
}

func main() {
	if adapter.ProbeRequested(os.Args) {
		adapter.RunProbe(contract.ProbeCheck{Name: contract.ProbeBackend, Passed: true, Message: "A2A 0.3 JSON-RPC; discovery, capabilities and authentication checked at invocation"})
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-a2a: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = adapter.WriteResponse(execute(ctx, req))
}

func validURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("A2A URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, fmt.Errorf("A2A requires HTTPS except on loopback")
	}
	return u, nil
}
func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host }
func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, adapter.MaxCaptureBytes+1))
	if len(b) > adapter.MaxCaptureBytes {
		return nil, fmt.Errorf("A2A response exceeds 4 MiB")
	}
	return b, err
}
func (c *client) request(ctx context.Context, method, endpoint string, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid A2A request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "0.3")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("A2A transport failed (submission outcome may be unknown)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, fmt.Errorf("A2A authentication rejected (HTTP %d); check A2A_TOKEN", resp.StatusCode)
		}
		return nil, fmt.Errorf("A2A HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func decodeRPC(raw []byte) (task, error) {
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil || r.JSONRPC != "2.0" || r.ID != "muxlio" {
		return task{}, fmt.Errorf("invalid A2A JSON-RPC response")
	}
	if r.Error != nil {
		return task{}, fmt.Errorf("A2A RPC error %d: %s", r.Error.Code, r.Error.Message)
	}
	var t task
	if err := json.Unmarshal(r.Result, &t); err != nil || t.Kind == "" {
		return task{}, fmt.Errorf("invalid A2A result")
	}
	return t, nil
}
func (c *client) rpc(ctx context.Context, method string, p any) (task, error) {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "method": method, "params": p})
	if err != nil {
		return task{}, err
	}
	resp, err := c.request(ctx, http.MethodPost, c.endpoint, raw, false)
	if err != nil {
		return task{}, err
	}
	defer resp.Body.Close()
	raw, err = readBounded(resp.Body)
	if err != nil {
		return task{}, err
	}
	return decodeRPC(raw)
}

func execute(parent context.Context, req *adapter.Request) adapter.Response {
	fail := func(err error) adapter.Response {
		return adapter.Response{Outputs: map[string]any{}, Error: "agent-a2a: " + err.Error()}
	}
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
	}
	if p.Timeout == "" {
		p.Timeout = "30s"
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil || timeout <= 0 || timeout > time.Minute {
		return fail(fmt.Errorf("timeout must be positive and at most 1m"))
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// A2A has no standard lookup/deduplication by message ID. Never resubmit an
	// uncertain operation or assert not-applied from a missing task.
	if req.Action == "reconcile" && (req.RemoteTask == nil || req.RemoteTask.ID == "") {
		return adapter.Response{Outputs: map[string]any{}, Reconciliation: &contract.Reconciliation{Status: "inconclusive", Reason: "A2A cannot locate an uncertain submission without a durable task ID"}}
	}
	for _, decl := range req.Outputs {
		if decl.Path != "" {
			if err := adapter.ValidateOutputPath(req.WorkDir, decl.Path); err != nil {
				return fail(err)
			}
		}
	}
	base, err := validURL(p.URL)
	if err != nil {
		return fail(err)
	}
	cardURL := p.CardURL
	if cardURL == "" {
		cardURL = base.Scheme + "://" + base.Host + "/.well-known/agent-card.json"
	}
	discovery, err := validURL(cardURL)
	if err != nil {
		return fail(err)
	}
	if !sameOrigin(base, discovery) {
		return fail(fmt.Errorf("Agent Card must share the configured URL origin"))
	}
	c := &client{http: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("redirects unsupported") }}, token: req.Secrets["A2A_TOKEN"]}
	resp, err := c.request(ctx, http.MethodGet, cardURL, nil, false)
	if err != nil {
		return fail(err)
	}
	raw, err := readBounded(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fail(err)
	}
	var card card
	if err = json.Unmarshal(raw, &card); err != nil {
		return fail(fmt.Errorf("invalid Agent Card"))
	}
	if card.ProtocolVersion != "0.3.0" {
		return fail(fmt.Errorf("unsupported A2A protocol %q; expected 0.3.0", card.ProtocolVersion))
	}
	endpoint := card.URL
	if card.PreferredTransport != "" && card.PreferredTransport != "JSONRPC" {
		endpoint = ""
		for _, i := range card.AdditionalInterfaces {
			if i.Transport == "JSONRPC" {
				endpoint = i.URL
				break
			}
		}
	}
	target, err := validURL(endpoint)
	if err != nil {
		return fail(fmt.Errorf("unsupported or invalid Agent Card JSONRPC endpoint: %w", err))
	}
	if !sameOrigin(base, target) {
		return fail(fmt.Errorf("Agent Card endpoint must share the configured URL origin"))
	}
	c.endpoint = endpoint
	if req.RemoteTask != nil && req.RemoteTask.URL != "" && req.RemoteTask.URL != endpoint {
		return fail(fmt.Errorf("durable task endpoint differs from discovered endpoint"))
	}
	for _, ext := range card.Capabilities.Extensions {
		if ext.Required {
			return fail(fmt.Errorf("unsupported required A2A extension %q", ext.URI))
		}
	}
	if p.Stream && !card.Capabilities.Streaming {
		return fail(fmt.Errorf("Agent Card does not support streaming"))
	}
	authorized := len(card.Security) == 0
	for _, alternative := range card.Security {
		supported := true
		for name, scopes := range alternative {
			scheme, ok := card.SecuritySchemes[name]
			if !ok || scheme.Type != "http" || !strings.EqualFold(scheme.Scheme, "bearer") || len(scopes) > 0 || c.token == "" {
				supported = false
			}
		}
		authorized = authorized || supported
	}
	if !authorized {
		return fail(fmt.Errorf("unsupported A2A authentication or missing A2A_TOKEN; only HTTP bearer security is supported"))
	}
	if req.Action == "" && req.RemoteTask == nil {
		textOK := false
		for _, mode := range card.DefaultInputModes {
			if mode == "text/plain" {
				textOK = true
			}
		}
		if !textOK {
			return fail(fmt.Errorf("Agent Card does not support text/plain input"))
		}
	}
	// Once a reference exists, every subsequent call is read-only, even if the
	// caller lost the action flag during recovery.
	if req.RemoteTask != nil {
		if req.RemoteTask.ID == "" {
			return fail(fmt.Errorf("remote task identity is empty"))
		}
		var streamErr error
		if p.Stream && req.Action != "reconcile" {
			// SSE is diagnostic only. Always obtain a full tasks/get snapshot after
			// reconnect/disconnect so missed and appended artifact chunks are not lost.
			streamCtx, stop := context.WithTimeout(ctx, timeout/2)
			streamErr = c.subscribe(streamCtx, req.RemoteTask)
			stop()
		}
		t, e := c.rpc(ctx, "tasks/get", map[string]any{"id": req.RemoteTask.ID})
		if e != nil {
			if req.Action == "reconcile" {
				return adapter.Response{Outputs: map[string]any{}, RemoteTask: req.RemoteTask, Reconciliation: &contract.Reconciliation{Status: "inconclusive", Reason: e.Error()}}
			}
			// A failed read does not terminate the remote operation.
			ref := *req.RemoteTask
			ref.Pending = true
			return adapter.Response{Outputs: map[string]any{}, RemoteTask: &ref, Logs: e.Error()}
		}
		if t.ID != req.RemoteTask.ID || (req.RemoteTask.ContextID != "" && t.ContextID != req.RemoteTask.ContextID) {
			return fail(fmt.Errorf("A2A task/context identity mismatch"))
		}
		result := finish(req, t, endpoint)
		if streamErr != nil {
			result.Logs = streamErr.Error()
		}
		if req.Action == "reconcile" {
			status := "completed"
			if result.RemoteTask == nil {
				status = "inconclusive"
			} else if result.RemoteTask.Pending {
				status = "pending"
			}
			result.Reconciliation = &contract.Reconciliation{Status: status}
		}
		return result
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return fail(err)
	}
	t, err := c.rpc(ctx, "message/send", map[string]any{
		"message":       map[string]any{"kind": "message", "role": "user", "messageId": fmt.Sprintf("%x", id), "parts": []any{map[string]any{"kind": "text", "text": remotePrompt(req)}}},
		"configuration": map[string]any{"blocking": false},
	})
	if err != nil {
		return fail(err)
	}
	return finish(req, t, endpoint)
}

func remotePrompt(req *adapter.Request) string {
	// Remote agents do not have access to worker filesystem paths.
	raw, _ := json.Marshal(req.Inputs)
	outputs := make(map[string]string)
	for port, decl := range req.Outputs {
		outputs[port] = decl.Mime
	}
	ports, _ := json.Marshal(outputs)
	return req.Task + "\nInputs (JSON): " + string(raw) + "\nReturn A2A artifacts named for each output port; scalar ports contain JSON. Output ports (MIME): " + string(ports)
}

func (c *client) subscribe(ctx context.Context, ref *contract.RemoteTaskReference) error {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "method": "tasks/resubscribe", "params": map[string]any{"id": ref.ID}})
	resp, err := c.request(ctx, http.MethodPost, c.endpoint, raw, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("A2A expected SSE")
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, adapter.MaxCaptureBytes+1))
	scanner.Buffer(make([]byte, 4096), adapter.MaxCaptureBytes)
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		t, e := decodeRPC([]byte(data.String()))
		data.Reset()
		if e != nil {
			return e
		}
		id := t.ID
		if id == "" {
			id = t.TaskID
		}
		if id != ref.ID || (ref.ContextID != "" && t.ContextID != ref.ContextID) {
			return fmt.Errorf("A2A stream identity mismatch")
		}
		adapter.AppendLogChunk("agent", "A2A task update: "+t.Status.State)
		if t.Status.Message != nil {
			for _, p := range t.Status.Message.Parts {
				if p.Kind == "text" {
					adapter.AppendStreamText(p.Text)
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

func finish(req *adapter.Request, t task, endpoint string) adapter.Response {
	r := adapter.Response{Outputs: map[string]any{}}
	if t.Kind == "message" {
		if len(req.Outputs) > 0 {
			r.Error = "agent-a2a: agent returned a message without required artifacts"
		}
		r.Outputs["_text"] = map[string]any{"mime": "text/plain", "data": partsText(t.Parts)}
		return r
	}
	if t.Kind != "task" || t.ID == "" || t.ContextID == "" {
		r.Error = "agent-a2a: invalid task identity"
		return r
	}
	r.RemoteTask = &contract.RemoteTaskReference{ID: t.ID, URL: endpoint, ContextID: t.ContextID}
	// Publish immediately, including before importing artifacts.
	if err := adapter.AppendOperationReference(*r.RemoteTask); err != nil {
		r.RemoteTask = nil // A publication failure does not confirm a remote outcome.
		r.Error = "agent-a2a: cannot publish durable task identity"
		return r
	}
	switch t.Status.State {
	case "submitted", "working":
		r.RemoteTask.Pending = true
		return r
	case "completed":
	case "failed", "canceled", "rejected":
		r.Error = "agent-a2a: remote task " + t.Status.State
		return r
	case "input-required", "auth-required":
		// These are interrupted, resumable A2A states, not terminal failures.
		// Keep the published identity for reconciliation without claiming the
		// remote operation has ended. This adapter cannot supply follow-up input.
		r.RemoteTask = nil
		r.Error = "agent-a2a: remote task " + t.Status.State + "; follow-up interaction is unsupported"
		return r
	default:
		r.RemoteTask = nil // An unrecognized state cannot confirm terminal failure.
		r.Error = "agent-a2a: unsupported remote task state " + t.Status.State
		return r
	}
	r.OutputErrors = map[string]string{}
	for port, decl := range req.Outputs {
		found := false
		for _, a := range t.Artifacts {
			if a.Name != port {
				continue
			}
			if found {
				r.OutputErrors[port] = "duplicate A2A artifact name"
				break
			}
			found = true
			data, e := artifactData(a.Parts)
			if e != nil {
				r.OutputErrors[port] = e.Error()
				continue
			}
			if decl.Path != "" {
				// Materialize bytes rather than JSON-encoding a possibly non-UTF8
				// string. The generic worker uploads the file before scratch cleanup.
				outputPath := filepath.Join(req.WorkDir, decl.Path)
				root, openErr := os.OpenRoot(req.WorkDir)
				if openErr != nil {
					r.OutputErrors[port] = "cannot open A2A artifact directory"
					continue
				}
				if err := root.MkdirAll(filepath.Dir(decl.Path), 0700); err != nil {
					root.Close()
					r.OutputErrors[port] = "cannot create A2A artifact directory"
					continue
				}
				file, writeErr := root.OpenFile(decl.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				root.Close()
				if writeErr != nil {
					r.OutputErrors[port] = "cannot create A2A artifact"
					continue
				}
				_, writeErr = file.Write(data)
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					os.Remove(file.Name())
					r.OutputErrors[port] = "cannot write A2A artifact"
					continue
				}
				r.Outputs[port] = map[string]any{"mime": decl.Mime, "path": outputPath}
			} else {
				var value any
				if e = json.Unmarshal(data, &value); e != nil {
					r.OutputErrors[port] = "A2A scalar artifact must contain JSON"
				} else {
					r.Outputs[port] = value
				}
			}
		}
		if !found {
			r.OutputErrors[port] = "missing A2A artifact"
		}
	}
	for port := range r.OutputErrors {
		if req.Outputs[port].Path != "" {
			r.Error = "agent-a2a: invalid or missing output artifacts"
			break
		}
	}
	if t.Status.Message != nil {
		r.Outputs["_text"] = map[string]any{"mime": "text/plain", "data": partsText(t.Status.Message.Parts)}
	}
	return r
}
func partsText(parts []part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
func artifactData(parts []part) ([]byte, error) {
	var b bytes.Buffer
	for _, p := range parts {
		switch p.Kind {
		case "text":
			b.WriteString(p.Text)
		case "data":
			if !json.Valid(p.Data) {
				return nil, fmt.Errorf("invalid A2A data part")
			}
			b.Write(p.Data)
		case "file":
			if p.File == nil || p.File.Bytes == nil || p.File.URI != "" {
				return nil, fmt.Errorf("A2A file artifacts require inline bytes; URI downloads unsupported")
			}
			data, err := base64.StdEncoding.DecodeString(*p.File.Bytes)
			if err != nil {
				return nil, fmt.Errorf("invalid A2A file bytes")
			}
			b.Write(data)
		default:
			return nil, fmt.Errorf("unsupported A2A artifact part %q", p.Kind)
		}
		if b.Len() > adapter.MaxCaptureBytes {
			return nil, fmt.Errorf("A2A artifact exceeds 4 MiB")
		}
	}
	return b.Bytes(), nil
}
