package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/coordinatorconfig"
)

// CheckCoordinator diagnoses the protected workers endpoint, since public health
// alone cannot establish that MCP's read tools can authenticate.
func CheckCoordinator(ctx context.Context, config, target, envServer, envToken string, connect bool) (string, error) {
	endpoint, token, err := coordinatorconfig.ResolveFile(config, target, envServer, envToken)
	if err != nil {
		return "", fmt.Errorf("coordinator configuration: %w; run muxlio init to migrate an existing local install, or select an explicit scoped profile", err)
	}
	if token == "" {
		return endpoint, fmt.Errorf("coordinator %s: no credential configured; run muxlio init for local setup or set the selected profile's scoped tokenFile", endpoint)
	}
	if !connect {
		return endpoint, nil
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/workers", nil)
	if err != nil {
		return endpoint, fmt.Errorf("invalid coordinator endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return endpoint, fmt.Errorf("coordinator %s: connection or TLS verification failed; check addr, certificates and muxlio service status", endpoint)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return endpoint, fmt.Errorf("coordinator %s: authentication rejected (HTTP 401); check the selected profile's credential", endpoint)
	case http.StatusForbidden:
		return endpoint, fmt.Errorf("coordinator %s: credential lacks workers read permission (HTTP 403); use a scoped profile with the required access", endpoint)
	case http.StatusOK:
		var body map[string]json.RawMessage
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body["registeredWorkers"] == nil {
			return endpoint, fmt.Errorf("coordinator %s: unexpected workers response; check the endpoint points to a Muxlio coordinator", endpoint)
		}
		return endpoint, nil
	default:
		return endpoint, fmt.Errorf("coordinator %s: workers endpoint returned HTTP %d; check the endpoint and service (redirects are refused)", endpoint, resp.StatusCode)
	}
}

// VerifyEndpoint uses the saved registration, not the command's repair defaults.
func VerifyEndpoint(ctx context.Context, o Options, connect bool) error {
	path, err := o.Path()
	if err != nil {
		return err
	}
	d, err := read(path, o.Client == "codex")
	if err != nil {
		return err
	}
	entry, _ := d.servers["muxlio"].(map[string]any)
	if entry["command"] == nil {
		return nil
	} // HTTP sessions are verified by the client.
	env, _ := entry["env"].(map[string]any)
	config, _ := env["MUXLIO_CONFIG"].(string)
	if config == "" {
		return fmt.Errorf("stdio registration has no explicit MUXLIO_CONFIG; use integrations repair after muxlio init")
	}
	target, envServer, token := "", os.Getenv("MUXLIO_SERVER"), os.Getenv("MUXLIO_TOKEN")
	if value, ok := env["MUXLIO_SERVER"].(string); ok {
		envServer = value
	}
	if value, ok := env["MUXLIO_TOKEN"].(string); ok {
		token = value
	}
	args, _ := entry["args"].([]any)
	for i, arg := range args {
		text, _ := arg.(string)
		if text == "--server" && i+1 < len(args) {
			target, _ = args[i+1].(string)
		}
		if strings.HasPrefix(text, "--server=") {
			target = strings.TrimPrefix(text, "--server=")
		}
	}
	_, err = CheckCoordinator(ctx, config, target, envServer, token, connect)
	return err
}
