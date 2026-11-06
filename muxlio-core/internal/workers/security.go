package workers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
)

// ValidatePermissions checks enforceable process boundaries. It makes no claim
// to intercept an agent's tool calls or to sandbox a provider's remote service.
// Legacy requests retain native execution; declaring any restriction requires
// an enforced isolation driver and fails closed before dispatch.
func ValidatePermissions(req ExecuteRequest) error {
	var err error
	req, err = executionSecurityConfig(req)
	if err != nil {
		return err
	}
	p := req.Policy
	if p == nil {
		return nil
	}
	if p.Trust != "" && p.Trust != "restricted" && p.Trust != "trusted-native" {
		return errors.New("invalid execution trust mode")
	}
	if p.Network != "" && p.Network != "none" && p.Network != "host" {
		return errors.New("invalid network permission")
	}
	if p.Filesystem != "" && p.Filesystem != "scratch" && p.Filesystem != "workspace-read-only" && p.Filesystem != "workspace-write" {
		return errors.New("invalid filesystem permission")
	}
	restricted := p.Trust == "restricted" || p.Network == "none" || p.Filesystem != ""
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) != nil {
		return errors.New("invalid execution permissions")
	}
	isolated := params.Isolation != nil && params.Isolation.Mode == "required"
	if restricted && (!isolated || p.Trust == "trusted-native") {
		return errors.New("restricted permissions require enforced isolation; trusted native execution is forbidden")
	}
	if isolated && (p.Trust == "trusted-native" || p.Network == "host") {
		return errors.New("isolation driver cannot grant requested native or network access")
	}
	if p.Filesystem == "scratch" && req.WorkspaceMode != "" && req.WorkspaceMode != "snapshot" {
		return errors.New("scratch permission forbids host workspace mounts")
	}
	if p.Filesystem == "workspace-read-only" && req.WorkspaceMode != "read-only" {
		return errors.New("read-only filesystem permission requires a read-only workspace")
	}
	if p.Filesystem == "workspace-write" && req.WorkspaceMode != "shared-write" {
		return errors.New("workspace write permission requires shared-write workspace admission")
	}
	// Restricted attempts default to scratch-only filesystem access.
	if p.Trust == "restricted" && p.Filesystem == "" && req.WorkspaceMode != "" && req.WorkspaceMode != "snapshot" {
		return errors.New("restricted host workspace access must be explicitly declared")
	}
	return nil
}

// Direct executor callers and coordinator callers share the same security
// declaration. A transport envelope cannot weaken the policy in node params.
func executionSecurityConfig(req ExecuteRequest) (ExecuteRequest, error) {
	var config struct {
		Policy     *ExecutionPolicy  `json:"executionPolicy"`
		SecretRefs map[string]string `json:"secretRefs"`
	}
	if len(req.Params) == 0 {
		return req, nil
	}
	if json.Unmarshal(req.Params, &config) != nil {
		return ExecuteRequest{}, errors.New("invalid execution security configuration")
	}
	if config.Policy != nil {
		if req.Policy != nil {
			a, _ := json.Marshal(req.Policy)
			b, _ := json.Marshal(config.Policy)
			if !bytes.Equal(a, b) {
				return ExecuteRequest{}, errors.New("conflicting execution policies")
			}
		}
		req.Policy = config.Policy
	}
	if config.SecretRefs != nil {
		if req.SecretRefs != nil {
			a, _ := json.Marshal(req.SecretRefs)
			b, _ := json.Marshal(config.SecretRefs)
			if !bytes.Equal(a, b) {
				return ExecuteRequest{}, errors.New("conflicting execution secret handles")
			}
		}
		req.SecretRefs = config.SecretRefs
	}
	return req, nil
}

// resolveSecrets runs on the execution host, using only operator-configured
// handles for this executor. Values never enter the durable assignment envelope.
func (s *ScriptExecutor) resolveSecrets(req ExecuteRequest) (ExecuteRequest, error) {
	req, err := GuardRequest(req)
	if err != nil {
		return ExecuteRequest{}, err
	}
	values := make(map[string]string, len(req.Secrets)+len(req.SecretRefs))
	for k, v := range req.Secrets {
		values[k] = v
	}
	for name, handle := range req.SecretRefs {
		key := s.Executors[req.Executor].SecretEnv[handle]
		value, ok := os.LookupEnv(key)
		if key == "" || !ok || value == "" {
			return ExecuteRequest{}, errors.New("execution secret handle unavailable or denied by worker policy")
		}
		values[name] = value
	}
	req.Secrets = values
	req.redactions = make(map[string]string, len(values))
	for k, v := range values {
		req.redactions[k] = v
	}
	for _, env := range []map[string]string{s.WorkerEnv, s.Executors[req.Executor].Env} {
		for k, v := range env {
			key := strings.ToLower(k)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "api_key") || strings.Contains(key, "apikey") || key == "authorization" {
				req.redactions["env:"+k] = v
			}
		}
	}
	return req, nil
}

func executionSecrets(req ExecuteRequest) map[string]string {
	if req.redactions != nil {
		return req.redactions
	}
	return req.Secrets
}

// Sanitize the complete result, including output metadata, operation references
// and usage extensions, before a caller can persist it. Walk JSON strings so
// credentials containing quotes or newlines cannot bypass replacement.
func sanitizeExecution(result ExecuteResult, err error, secrets map[string]string) (ExecuteResult, error) {
	if len(secrets) == 0 {
		return result, err
	}
	if err != nil {
		var detail *diagnostic.Diagnostic
		if errors.As(err, &detail) {
			var safe diagnostic.Diagnostic
			if sanitizeJSON(detail, &safe, secrets) != nil {
				return ExecuteResult{}, errors.New("cannot sanitize execution diagnostic")
			}
			err = &safe
		} else if message := redactValues(err.Error(), secrets); message != err.Error() {
			err = errors.New(message)
		}
	}
	var safe ExecuteResult
	if sanitizeJSON(result, &safe, secrets) != nil {
		return ExecuteResult{}, errors.New("cannot sanitize execution result")
	}
	return safe, err
}

func sanitizeJSON(input, output any, secrets map[string]string) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case string:
			return redactValues(x, secrets)
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, item := range x {
				out[redactValues(k, secrets)] = clean(item)
			}
			return out
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
		}
		return v
	}
	raw, err = json.Marshal(clean(value))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func redactValues(value string, secrets map[string]string) string {
	// RedactSecrets sorts longest values first; append a sentinel to avoid its
	// stream-tail handling truncating ordinary result strings.
	const sentinel = "\x00"
	return strings.TrimSuffix(RedactSecrets(value+sentinel, secrets), sentinel)
}

func containsSecret(data []byte, secrets map[string]string) bool {
	for _, value := range secrets {
		if value != "" && bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	return false
}

// credentialWriter scans across copy chunk boundaries before bytes can be
// published. Keep only the suffix needed to recognize the longest credential.
type credentialWriter struct {
	dst     io.Writer
	secrets map[string]string
	tail    []byte
}

func (w *credentialWriter) Write(p []byte) (int, error) {
	window := append(w.tail, p...)
	if containsSecret(window, w.secrets) {
		return 0, errors.New("artifact publication denied: output contains execution credentials")
	}
	overlap := 0
	for _, value := range w.secrets {
		if len(value) > overlap {
			overlap = len(value) - 1
		}
	}
	if overlap > len(window) {
		overlap = len(window)
	}
	w.tail = bytes.Clone(window[len(window)-overlap:])
	return w.dst.Write(p)
}
