package workers

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestSecretHandlesResolveOnlyAtExecution(t *testing.T) {
	const secret = "execution-credential-93874"
	t.Setenv("TEST_EXECUTION_CREDENTIAL", secret)
	s := testScript(t, `request=$(cat)
case "$request" in *execution-credential-93874*) ;; *) exit 7 ;; esac
printf '%s\n' '{"stream":"agent","text":"execution-credential-93874"}' >&3
printf '%s' '{"outputs":{"manifest":{"path":"execution-credential-93874","data":"execution-credential-93874"}},"error":"execution-credential-93874","logs":"execution-credential-93874"}'`)
	s.Executors = map[string]ExecutorConfig{"test": {SecretEnv: map[string]string{"service-credential": "TEST_EXECUTION_CREDENTIAL"}}}
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run", "activation", nil)
	req := ExecuteRequest{Executor: "test", SecretRefs: map[string]string{"api": "service-credential"}, Policy: &ExecutionPolicy{Required: true, AllowedCapabilities: []string{"test"}, AllowedSecrets: []string{"api"}}, LogSink: sink}
	raw, err := json.Marshal(req)
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("unsafe assignment: %s %v", raw, err)
	}
	result, err := s.Execute(req)
	if err != nil || result.Error != "[REDACTED]" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	sink.Close()
	raw, _ = json.Marshal(struct {
		Result ExecuteResult
		Logs   []state.LogChunk
	}{result, rec.chunks})
	if strings.Contains(string(raw), secret) || len(rec.chunks) == 0 {
		t.Fatalf("credential persisted or missing stream: %s", raw)
	}
	for _, chunk := range rec.chunks {
		if strings.Contains(string(chunk.Data), secret) {
			t.Fatal("stream leaked credential")
		}
	}
	// An approved name does not authorize a different operator handle.
	req.SecretRefs["api"] = "unapproved"
	result, err = s.Execute(req)
	if err != nil || !strings.Contains(result.Error, "denied") {
		t.Fatalf("unapproved handle ran: %+v %v", result, err)
	}
}

func TestExecutionCredentialsNeverSerialize(t *testing.T) {
	const secret = "opaque-sensitive-value"
	raw, err := json.Marshal(ExecuteRequest{Secrets: map[string]string{"api": secret}})
	if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "secrets") {
		t.Fatalf("raw secret serialized: %s %v", raw, err)
	}
	var req ExecuteRequest
	if err := json.Unmarshal([]byte(`{"secrets":{"api":"opaque-sensitive-value"}}`), &req); err != nil || len(req.Secrets) != 0 {
		t.Fatal("wire credential accepted")
	}
	result, err := sanitizeExecution(ExecuteResult{Outputs: map[string]any{secret: map[string]any{"path": secret}}, Error: secret}, errors.New(secret), map[string]string{"api": secret})
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), secret) || strings.Contains(err.Error(), secret) {
		t.Fatal("secret survived result sanitization")
	}
}

func TestRestrictedPermissionsCannotUseNativeFallback(t *testing.T) {
	s := testScript(t, "exit 42")
	for _, params := range []string{`{}`, `{"isolation":{"mode":"trusted-native"}}`, `{"isolation":{"mode":"required","runtime":"unknown"}}`} {
		req := ExecuteRequest{Executor: "test", Policy: &ExecutionPolicy{Trust: "restricted", Network: "none", Filesystem: "scratch"}, Params: json.RawMessage(params)}
		cmd, _, err := s.command(req, s.scriptPath("test"), t.TempDir())
		if err == nil || cmd != nil {
			t.Fatalf("restricted attempt allowed native execution for %s", params)
		}
	}
	req := ExecuteRequest{Executor: "test", Policy: &ExecutionPolicy{Trust: "restricted"}, Params: json.RawMessage(`{"isolation":{"mode":"required","runtime":"bwrap"}}`), WorkspaceMode: "shared-write"}
	if err := ValidatePermissions(req); err == nil {
		t.Fatal("undeclared workspace write accepted")
	}
	if err := ValidatePermissions(ExecuteRequest{Policy: &ExecutionPolicy{Filesystem: "workspace-write"}, WorkspaceMode: "shared-write"}); err == nil {
		t.Fatal("workspace-only write declaration silently granted host filesystem access")
	}
	raw, _ := json.Marshal(ExecuteRequest{Policy: &ExecutionPolicy{Trust: "restricted", Network: "none"}})
	if MatchDispatch("test", raw, []state.WorkerCapability{{Name: "test"}}, nil) {
		t.Fatal("native worker admitted restricted attempt")
	}
}

func TestDirectExecutionCannotWeakenParamsPolicy(t *testing.T) {
	s := testScript(t, `printf '{"outputs":{"executed":true}}'`)
	for _, policy := range []*ExecutionPolicy{nil, {Trust: "trusted-native"}} {
		req := ExecuteRequest{Executor: "test", Policy: policy, Params: json.RawMessage(`{"executionPolicy":{"trust":"restricted"}}`)}
		result, err := s.Execute(req)
		if err != nil || result.Error == "" || len(result.Outputs) != 0 {
			t.Fatalf("restriction bypass: %+v %v", result, err)
		}
	}
}

func TestConfiguredEnvironmentCredentialsAreRedacted(t *testing.T) {
	s := testScript(t, `printf '{"error":"%s","outputs":{"manifest":{"description":"%s"}}}' "$SERVICE_TOKEN" "$SERVICE_TOKEN"`)
	s.WorkerEnv = map[string]string{"SERVICE_TOKEN": "operator-environment-credential"}
	result, err := s.Execute(ExecuteRequest{Executor: "test"})
	raw, _ := json.Marshal(result)
	if err != nil || result.Error != "[REDACTED]" || strings.Contains(string(raw), "operator-environment-credential") {
		t.Fatalf("unsafe environment output: %s %v", raw, err)
	}
}

func TestCredentialArtifactPublicationDenied(t *testing.T) {
	dir := t.TempDir()
	const secret = "artifact-credential-value"
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	req := ExecuteRequest{attemptScratch: dir, Secrets: map[string]string{"api": secret}, Outputs: map[string]OutputDecl{"report": {Path: "report.txt"}}}
	result, err := (ArtifactTransferExecutor{}).publishOutputs(req, ExecuteResult{})
	if err != nil || !strings.Contains(result.Error, "publication denied") || strings.Contains(result.Error, secret) {
		t.Fatalf("unsafe artifact publication: %+v %v", result, err)
	}
}

func TestEscapedOperationCredentialsAreRedacted(t *testing.T) {
	const secret = "credential\"with\ncontrols"
	s := testScript(t, `cat >/dev/null
printf '%s\n' '{"operationRef":{"id":"credential\"with\ncontrols","url":"https://example.test/task"}}' >&3
printf '%s' '{"outputs":{}}'`)
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run", "activation", nil)
	_, err := s.Execute(ExecuteRequest{Executor: "test", Secrets: map[string]string{"api": secret}, Policy: &ExecutionPolicy{AllowedSecrets: []string{"api"}}, LogSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	sink.Close()
	if len(rec.chunks) != 1 {
		t.Fatalf("expected operation log, got %d", len(rec.chunks))
	}
	var operation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.chunks[0].Data, &operation); err != nil || operation.ID != "[REDACTED]" {
		t.Fatalf("unsafe operation log: %s (%v)", rec.chunks[0].Data, err)
	}
}

func TestArtifactPreparationErrorsAreRedacted(t *testing.T) {
	const secret = "artifact-error-credential"
	s := testScript(t, "exit 1")
	dir := t.TempDir()
	blocker := filepath.Join(dir, secret)
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := ArtifactTransferExecutor{Inner: s, WorkBase: blocker}
	result, err := a.Execute(ExecuteRequest{Executor: "test", Secrets: map[string]string{"api": secret}, Policy: &ExecutionPolicy{AllowedSecrets: []string{"api"}}})
	raw, _ := json.Marshal(result)
	if err == nil && result.Error == "" {
		t.Fatal("expected scratch preparation failure")
	}
	if strings.Contains(string(raw), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
		t.Fatal("artifact preparation leaked credential")
	}
}

func TestArtifactCredentialScannerAcrossChunks(t *testing.T) {
	var dst bytes.Buffer
	w := &credentialWriter{dst: &dst, secrets: map[string]string{"api": "split-credential"}}
	if _, err := w.Write([]byte("safe prefix split-")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("credential suffix")); err == nil || strings.Contains(err.Error(), "split-credential") {
		t.Fatalf("expected safe denial for split credential: %v", err)
	}
	// Large safe artifacts retain their existing supported size.
	w = &credentialWriter{dst: &dst, secrets: map[string]string{"api": "split-credential"}}
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 80; i++ {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("large safe artifact rejected: %d %v", n, err)
		}
	}
}

func TestCredentialRedactionPreservesExecutionErrorContracts(t *testing.T) {
	secrets := map[string]string{"api": "execution-credential"}
	_, err := sanitizeExecution(ExecuteResult{}, UncertainExecutionError{}, secrets)
	var uncertain UncertainExecutionError
	if !errors.As(err, &uncertain) {
		t.Fatalf("lost reconciliation-required error: %v", err)
	}
	original := diagnostic.New("MUXLIO_ADAPTER_NOT_FOUND", "runtime", "missing adapter", "Install adapter")
	original.Expected = map[string]any{"path": "execution-credential"}
	_, err = sanitizeExecution(ExecuteResult{}, &original, secrets)
	var detail *diagnostic.Diagnostic
	if !errors.As(err, &detail) || detail.Code != original.Code || detail.Fix != original.Fix {
		t.Fatalf("lost structured diagnostic: %v", err)
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), secrets["api"]) || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("unsafe structured diagnostic: %s", raw)
	}
	// Exercise the actual executor path with configured credentials as well.
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	s.WorkerEnv = map[string]string{"SERVICE_TOKEN": secrets["api"]}
	_, err = s.Execute(ExecuteRequest{Executor: "generic/missing"})
	if !errors.As(err, &detail) || detail.Code != "MUXLIO_ADAPTER_NOT_FOUND" {
		t.Fatalf("executor lost diagnostic when credentials configured: %v", err)
	}
}
