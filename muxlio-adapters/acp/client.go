// Package acp implements the ACP v1 stdio client used by the shared executor.
// ACP sessions are connection-local here, not Muxlio durable session handles.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// Capabilities is the negotiated agent capability set. Unknown capabilities
// remain visible but never imply support for a Muxlio executor capability.
type Capabilities map[string]json.RawMessage

type Client struct {
	reader       io.Reader
	writer       *json.Encoder
	incoming     chan message
	done         chan struct{}
	err          error
	next         int
	session      string
	Capabilities Capabilities
	// Update receives validated session updates, including future variants.
	Update             func(json.RawMessage) error
	Negotiated         func(Capabilities)
	SessionReady       func(string)
	ModelSelected      func(string)
	PromptStarted      func(string)
	pendingUpdates     []message
	pendingUpdateBytes int
}

func New(reader io.Reader, writer io.Writer) *Client {
	c := &Client{reader: reader, writer: json.NewEncoder(writer), incoming: make(chan message), done: make(chan struct{})}
	return c
}

// read owns the reader until EOF or ctx cancellation. Lines are bounded so a
// malformed or noisy agent cannot grow memory without limit.
func (c *Client) read(ctx context.Context, reader io.Reader) {
	defer close(c.done)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var m message
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil || m.JSONRPC != "2.0" || (m.Method != "" && (len(m.Result) > 0 || len(m.Error) > 0)) {
			c.err = fmt.Errorf("invalid ACP JSON-RPC message")
			return
		}
		select {
		case c.incoming <- m:
		case <-ctx.Done():
			return
		}
	}
	c.err = scanner.Err()
	if c.err == nil {
		c.err = io.ErrUnexpectedEOF
	}
}

// The owner must close the transport after Execute returns, releasing any
// blocked read/write. A stalled agent must not prevent deadline cancellation.
func (c *Client) send(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ACP cancelled: %w", err)
	}
	written := make(chan error, 1)
	go func() { written <- c.writer.Encode(value) }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		return fmt.Errorf("ACP cancelled: %w", ctx.Err())
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.next++
	id := c.next
	if err := c.send(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	cancelled := false
	cancel := ctx.Done()
	var deadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-cancel:
			if method != "session/prompt" {
				return fmt.Errorf("ACP cancelled: %w", ctx.Err())
			}
			grace, stop := context.WithTimeout(context.Background(), time.Second)
			err := c.send(grace, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": c.session}})
			stop()
			if err != nil {
				return fmt.Errorf("ACP cancelled: %w", err)
			}
			cancelled, cancel = true, nil
			timer = time.NewTimer(time.Second)
			deadline = timer.C
		case <-deadline:
			return fmt.Errorf("ACP cancelled; agent did not acknowledge cancellation")
		case <-c.done:
			if cancelled {
				return fmt.Errorf("ACP cancelled: %v", c.err)
			}
			return fmt.Errorf("ACP %s: %w", method, c.err)
		case m := <-c.incoming:
			if m.Method != "" {
				// A new agent session may publish initial context/cost before
				// its response. Bind these updates to the returned session ID
				// before exposing them as a pre-prompt baseline.
				if method == "session/new" && m.Method == "session/update" {
					if len(c.pendingUpdates) >= 64 || c.pendingUpdateBytes+len(m.Params) > 64<<10 {
						return fmt.Errorf("ACP initial session updates exceed bound")
					}
					c.pendingUpdates = append(c.pendingUpdates, m)
					c.pendingUpdateBytes += len(m.Params)
					continue
				}
				if err := c.handle(ctx, m); err != nil {
					return err
				}
				continue
			}
			if len(m.Params) > 0 || (len(m.Result) > 0) == (len(m.Error) > 0) {
				return fmt.Errorf("invalid ACP response envelope")
			}
			if !jsonRPCIDEqual(id, m.ID) {
				return fmt.Errorf("ACP response ID mismatch")
			}
			if cancelled || ctx.Err() != nil {
				return fmt.Errorf("ACP cancelled")
			}
			if len(m.Error) > 0 {
				return fmt.Errorf("ACP %s failed: %s", method, m.Error)
			}
			if len(m.Result) == 0 || string(m.Result) == "null" {
				return fmt.Errorf("ACP %s missing result", method)
			}
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("ACP %s result: %w", method, err)
			}
			return nil
		}
	}
}

// jsonRPCIDEqual reports whether a JSON-RPC response id matches the client's request id.
func jsonRPCIDEqual(want int, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if string(raw) == fmt.Sprint(want) {
		return true
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	switch v := decoded.(type) {
	case float64:
		return v == float64(want)
	default:
		return false
	}
}

func (c *Client) handle(ctx context.Context, m message) error {
	if m.Method == "session/update" {
		var p struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind string `json:"sessionUpdate"`
			} `json:"update"`
		}
		if len(m.ID) > 0 || json.Unmarshal(m.Params, &p) != nil || c.session == "" || p.SessionID != c.session || p.Update.Kind == "" {
			return fmt.Errorf("invalid ACP session/update")
		}
		var raw struct {
			Update json.RawMessage `json:"update"`
		}
		_ = json.Unmarshal(m.Params, &raw)
		if c.Update != nil {
			return c.Update(raw.Update)
		}
		return nil
	}
	if len(m.ID) == 0 {
		return nil
	} // Unknown notifications are extensible.
	if m.Method == "session/request_permission" {
		if err := c.send(ctx, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}}); err != nil {
			return err
		}
		return fmt.Errorf("ACP permission requests are unsupported by this executor; permission denied")
	}
	return c.send(ctx, map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "client capability unsupported"}})
}

// Execute is single-use; the caller closes the transport when it returns.
// It negotiates v1 and creates a fresh session. Authentication is managed
// by the configured agent; auth_required is surfaced without starting a login.
func (c *Client) Execute(ctx context.Context, cwd, prompt string) error {
	readCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go c.read(readCtx, c.reader)
	var init struct {
		Version      int          `json:"protocolVersion"`
		Capabilities Capabilities `json:"agentCapabilities"`
	}
	if err := c.call(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "muxlio", "version": "1"}}, &init); err != nil {
		return err
	}
	if init.Version != 1 {
		return fmt.Errorf("unsupported ACP protocol version %d (supported: 1)", init.Version)
	}
	if init.Capabilities == nil {
		return fmt.Errorf("ACP initialize missing agentCapabilities")
	}
	// Validate known capability shapes without rejecting future extensions.
	var known struct {
		LoadSession bool `json:"loadSession"`
		Prompt      *struct {
			Image           bool `json:"image"`
			Audio           bool `json:"audio"`
			EmbeddedContext bool `json:"embeddedContext"`
		} `json:"promptCapabilities"`
		MCP *struct {
			HTTP bool `json:"http"`
			SSE  bool `json:"sse"`
		} `json:"mcpCapabilities"`
		Session map[string]json.RawMessage `json:"sessionCapabilities"`
	}
	raw, _ := json.Marshal(init.Capabilities)
	if err := json.Unmarshal(raw, &known); err != nil {
		return fmt.Errorf("invalid ACP agentCapabilities: %w", err)
	}
	c.Capabilities = init.Capabilities
	if c.Negotiated != nil {
		c.Negotiated(c.Capabilities)
	}
	var session struct {
		ID            string          `json:"sessionId"`
		ConfigOptions json.RawMessage `json:"configOptions"`
	}
	if err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &session); err != nil {
		return err
	}
	if session.ID == "" {
		return fmt.Errorf("ACP session/new missing sessionId")
	}
	c.session = session.ID
	if c.ModelSelected != nil {
		for _, model := range selectedModelsAtSessionSetup(session.ConfigOptions, c.pendingUpdates) {
			c.ModelSelected(model)
		}
	}
	if c.SessionReady != nil {
		c.SessionReady(c.session)
	}
	for _, m := range c.pendingUpdates {
		if err := c.handle(ctx, m); err != nil {
			return err
		}
	}
	c.pendingUpdates = nil
	c.pendingUpdateBytes = 0
	if c.PromptStarted != nil {
		c.PromptStarted(c.session)
	}
	var result struct {
		Stop string `json:"stopReason"`
	}
	if err := c.call(ctx, "session/prompt", map[string]any{"sessionId": c.session, "prompt": []any{map[string]any{"type": "text", "text": prompt}}}, &result); err != nil {
		return err
	}
	if result.Stop != "end_turn" {
		return fmt.Errorf("ACP prompt stopped: %q", result.Stop)
	}
	return nil
}
