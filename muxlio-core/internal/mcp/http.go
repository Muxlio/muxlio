package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const maxMCPRequestBytes = 4 << 20

type allowedOrigins []string

func (o *allowedOrigins) String() string         { return strings.Join(*o, ",") }
func (o *allowedOrigins) Set(value string) error { *o = append(*o, value); return nil }

// newHTTPHandler uses independent JSON-RPC encoders for concurrent requests.
// No session IDs or server-push streams are needed for these request/response tools.
func newHTTPHandler(tools *server, token string, origins []string) (http.Handler, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("HTTP transport requires a bearer token: configure coordinator credentials, --http-token-file, or MUXLIO_MCP_HTTP_TOKEN")
	}
	allowed := map[string]bool{}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid allowed Origin %q: use an exact HTTP(S) origin without a path", origin)
		}
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if origins := r.Header.Values("Origin"); len(origins) > 0 {
			if len(origins) != 1 || !allowed[origins[0]] {
				http.Error(w, "Origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origins[0])
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Expose-Headers", "MCP-Protocol-Version")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		auth := strings.Fields(r.Header.Get("Authorization"))
		if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || subtle.ConstantTimeCompare([]byte(auth[1]), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="muxlio-mcp"`)
			http.Error(w, "invalid or missing bearer token", http.StatusUnauthorized)
			return
		}
		version := r.Header.Get("MCP-Protocol-Version")
		if version != "" && version != mcpProtocolVersion {
			http.Error(w, "unsupported MCP-Protocol-Version", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			// Streamable HTTP permits 405 when GET streams or DELETE sessions
			// are not provided. This server is stateless and replies with JSON.
			w.Header().Set("Allow", "POST, OPTIONS")
			http.Error(w, "use POST for MCP messages; no SSE stream or sessions are provided", http.StatusMethodNotAllowed)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		if !acceptsMedia(r.Header.Get("Accept"), "application/json") || !acceptsMedia(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "Accept must include application/json and text/event-stream", http.StatusNotAcceptable)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPRequestBytes))
		if err != nil {
			var limitError *http.MaxBytesError
			if errors.As(err, &limitError) {
				http.Error(w, "MCP request exceeds 4 MiB", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "could not read MCP request", http.StatusBadRequest)
			}
			return
		}
		message, response, code, detail := decodeHTTPMessage(body)
		if code != 0 {
			writeHTTPRPCError(w, http.StatusBadRequest, nil, code, detail)
			return
		}
		if version == "" && message.Method != "initialize" {
			http.Error(w, "MCP-Protocol-Version is required after initialization", http.StatusBadRequest)
			return
		}
		if response || len(message.ID) == 0 {
			// Notifications and client responses have no JSON-RPC response body.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if message.Method == "initialize" {
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil || params.ProtocolVersion == "" {
				writeHTTPRPCError(w, http.StatusOK, message.ID, -32602, "initialize requires protocolVersion")
				return
			}
		}
		var out bytes.Buffer
		rpc := &mcpServer{tools: tools, enc: json.NewEncoder(&out)}
		rpc.handle(message)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Protocol-Version", mcpProtocolVersion)
		_, _ = w.Write(out.Bytes())
	}), nil
}

func acceptsMedia(header, wanted string) bool {
	for _, entry := range strings.Split(header, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(entry))
		if err != nil {
			continue
		}
		if q, exists := params["q"]; exists {
			quality, err := strconv.ParseFloat(q, 64)
			if err != nil || quality <= 0 || quality > 1 {
				continue
			}
		}
		if media == wanted || media == "*/*" || media == strings.SplitN(wanted, "/", 2)[0]+"/*" {
			return true
		}
	}
	return false
}

func decodeHTTPMessage(body []byte) (message jsonRPCRequest, response bool, code int, detail string) {
	if !utf8.Valid(body) || !json.Valid(body) {
		return message, false, -32700, "invalid JSON"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return message, false, -32600, "expected one JSON-RPC object, not a batch"
	}
	if err := json.Unmarshal(body, &message); err != nil || message.JSONRPC != "2.0" {
		return message, false, -32600, "invalid JSON-RPC message"
	}
	if id, exists := fields["id"]; exists {
		var value any
		_ = json.Unmarshal(id, &value)
		switch value.(type) {
		case string, float64:
		default:
			return message, false, -32600, "id must be a string or number"
		}
	}
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if message.Method == "" {
		if len(message.ID) > 0 && hasResult != hasError {
			return message, true, 0, ""
		}
		return message, false, -32600, "invalid JSON-RPC response"
	}
	if hasResult || hasError {
		return message, false, -32600, "requests cannot contain result or error"
	}
	if len(message.ID) == 0 && !strings.HasPrefix(message.Method, "notifications/") {
		return message, false, -32600, "MCP requests require an id"
	}
	return message, false, 0, ""
}

func writeHTTPRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: detail}})
}

func httpListenerToken(file, coordinatorToken string) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading HTTP token file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if token := strings.TrimSpace(os.Getenv("MUXLIO_MCP_HTTP_TOKEN")); token != "" {
		return token, nil
	}
	return coordinatorToken, nil
}

func runHTTP(tools *server, addr, tokenFile, coordinatorToken, cert, key string, origins []string) error {
	token, err := httpListenerToken(tokenFile, coordinatorToken)
	if err != nil {
		return err
	}
	handler, err := newHTTPHandler(tools, token, origins)
	if err != nil {
		return err
	}
	listener, err := listenMCPHTTP(addr, cert, key)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	scheme := "http"
	if cert != "" {
		scheme = "https"
	}
	fmt.Fprintf(os.Stderr, "muxlio-mcp: endpoint=%s://%s/mcp coordinator=%s\n", scheme, listener.Addr(), tools.api.base)
	return serveMCPHTTP(ctx, listener, handler)
}

func listenMCPHTTP(addr, cert, key string) (net.Listener, error) {
	if (cert == "") != (key == "") {
		return nil, errors.New("--tls-cert and --tls-key must be supplied together")
	}
	var tlsConfig *tls.Config
	if cert != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("loading MCP TLS identity: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	return listener, nil
}

func serveMCPHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := NewHTTPServer(handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

// NewHTTPHandler constructs an authenticated MCP endpoint with explicit upstream
// credentials. It does not resolve client profiles or infer a coordinator.
func NewHTTPHandler(coordinatorURL, coordinatorToken, tokenFile string, origins []string) (http.Handler, error) {
	token, err := httpListenerToken(tokenFile, coordinatorToken)
	if err != nil {
		return nil, err
	}
	return newHTTPHandler(&server{api: newAPIClient(coordinatorURL, coordinatorToken)}, token, origins)
}

// ListenHTTP binds the MCP endpoint and validates its optional TLS identity.
func ListenHTTP(addr, cert, key string) (net.Listener, error) {
	return listenMCPHTTP(addr, cert, key)
}

// NewHTTPServer provides the same transport limits in standalone and service mode.
func NewHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}
