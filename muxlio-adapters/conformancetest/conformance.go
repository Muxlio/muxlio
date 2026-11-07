// Package conformancetest is a deterministic executor contract test kit.
// Fixture data belongs to the adapter; all lifecycle assertions are shared.
package conformancetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// Fixture supplies a provider's wire format, never provider-specific assertions.
// RawOutput executors accept arbitrary stdout and do not report model usage.
type Fixture struct {
	// Lifecycle supplies transport-owned fixtures for adapters that do not spawn
	// a CLI. The shared harness still builds and validates their manifests.
	Lifecycle                                func(t *testing.T, binary string, manifest *contract.AdapterManifest)
	Executor, Binary, CLIName, SuccessStream string
	// MalformedStreams are provider-shaped events with invalid payloads.
	MalformedStreams []string
	RawOutput        bool
	// UsageUnavailable separates structured protocols without token usage from raw stdout.
	UsageUnavailable bool
	// Script and CancelScript supply interactive provider fixtures when needed.
	Script          func(stream, stderr string, code int, scratch string) string
	CancelScript    string
	CancelReadyText string
}

// Run builds each adapter and exercises the same baseline lifecycle. The only
// executables on the fixture PATH are deterministic stubs; no credentials or
// network access are used. Every subprocess has a deadline.
func Run(t *testing.T, moduleRoot string, fixtures []Fixture) {
	t.Helper()
	bin := t.TempDir()
	for _, f := range fixtures {
		t.Run(f.Executor, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, f.Binary), "./cmd/"+f.Binary)
			cmd.Dir = moduleRoot
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			raw, err := os.ReadFile(filepath.Join(moduleRoot, "cmd", f.Binary, "adapter.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := contract.ParseManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := manifest.Check(f.Executor, "", contract.Version, nil); err != nil {
				t.Fatal(err)
			}
			if manifest.Supports(contract.CapabilityUsage) == (f.RawOutput || f.UsageUnavailable) {
				t.Fatal("usage declaration disagrees with fixture")
			}
			if f.Lifecycle != nil {
				f.Lifecycle(t, filepath.Join(bin, f.Binary), manifest)
				return
			}
			for _, capability := range []string{contract.CapabilitySession, contract.CapabilityRemoteTask, contract.CapabilityTypedResult} {
				if err := manifest.Check(f.Executor, "", contract.Version, map[string]string{capability: "1"}); err == nil {
					t.Fatalf("unsupported %s must be explicit", capability)
				}
				t.Run("unsupported_"+capability, func(t *testing.T) {
					resp, progress := invoke(t, bin, f, f.SuccessStream, "", 0, false, nil, func(req *adapter.Request) {
						req.RequiredCapabilities = map[string]string{capability: contract.Version}
					})
					if !strings.Contains(resp.Error, capability) || progress != "" {
						t.Fatalf("unsupported feature was dispatched: %+v %q", resp, progress)
					}
				})
			}
			if manifest.Supports(contract.CapabilityCancel) {
				t.Run("graceful_cancel", func(t *testing.T) { testGracefulCancel(t, bin, f) })
			} else {
				t.Run("cancel_unsupported", func(t *testing.T) {
					resp, _ := invoke(t, bin, f, f.SuccessStream, "", 0, false, nil, func(req *adapter.Request) {
						req.RequiredCapabilities = map[string]string{contract.CapabilityCancel: contract.Version}
					})
					if !strings.Contains(resp.Error, contract.CapabilityCancel) {
						t.Fatalf("cancel was silently emulated: %+v", resp)
					}
				})
			}
			cases := []struct {
				name, stream, stderr string
				code                 int
				fail                 bool
			}{
				{"success_with_stderr", f.SuccessStream, "diagnostic", 0, false},
				{"nonzero_exit", f.SuccessStream, "provider failed", 1, true},
				{"crash", "", "fatal crash", -1, true},
				{"redaction", "", "SUPER_SECRET_API_KEY_xyz123", 1, true},
				{"success_redaction", f.SuccessStream, "SUPER_SECRET_API_KEY_xyz123", 0, false},
				{"stdout_redaction", secretStream(t, f), "", 0, false},
				{"empty_output", "", "", 0, !f.RawOutput},
				{"malformed_output", "NOT JSON\n{broken}", "", 0, !f.RawOutput},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					resp, progress := invoke(t, bin, f, tc.stream, tc.stderr, tc.code, false, nil)
					if (resp.Error != "") != tc.fail {
						t.Fatalf("error = %q, want failure %v", resp.Error, tc.fail)
					}
					if strings.Contains(resp.Error+resp.Logs+progress, "SUPER_SECRET_API_KEY_xyz123") {
						t.Fatal("secret leaked into diagnostics")
					}
					if tc.name == "success_with_stderr" || tc.name == "nonzero_exit" {
						if f.RawOutput || f.UsageUnavailable {
							if resp.Usage != nil {
								t.Fatal("usage must be unavailable")
							}
						} else if resp.Usage == nil || *resp.Usage != (contract.TokenUsage{Input: 120, Output: 30, Cached: 40}) {
							t.Fatalf("usage = %+v", resp.Usage)
						}
					}
					if tc.name == "crash" && resp.Usage != nil {
						t.Fatal("crash without reported usage must keep usage unavailable")
					}
					if tc.name == "success_with_stderr" {
						if !strings.Contains(resp.Logs+progress, "diagnostic") {
							t.Fatal("stderr diagnostics lost")
						}
						if len(resp.Outputs) == 0 {
							t.Fatal("missing result outputs")
						}
						if manifest.Supports(contract.CapabilityProgress) && progress == "" {
							t.Fatal("declared progress capability produced no progress")
						}
					}
				})
			}
			for i, stream := range f.MalformedStreams {
				t.Run(fmt.Sprintf("malformed_payload_%d", i), func(t *testing.T) {
					resp, _ := invoke(t, bin, f, stream, "", 0, false, nil)
					if resp.Error == "" {
						t.Fatal("malformed provider payload accepted as success")
					}
				})
			}
			for name, mutate := range map[string]func(*adapter.Request){
				"contract_version": func(r *adapter.Request) { r.ContractVersion = "99" },
				"executor_name":    func(r *adapter.Request) { r.Executor = "incompatible/executor" },
				"executor_version": func(r *adapter.Request) { r.ExecutorVersion = "99" },
				"capability_version": func(r *adapter.Request) {
					r.RequiredCapabilities = map[string]string{contract.CapabilityProgress: "99"}
				},
				"session_reference": func(r *adapter.Request) { r.SessionID = "existing" },
				"legacy_session":    func(r *adapter.Request) { r.Params = json.RawMessage(`{"sessionId":"existing"}`) },
				"remote_reference":  func(r *adapter.Request) { r.RemoteTask = &contract.RemoteTaskReference{ID: "existing"} },
			} {
				t.Run("reject_"+name, func(t *testing.T) {
					resp, _ := invoke(t, bin, f, f.SuccessStream, "", 0, false, nil, mutate)
					if resp.Error == "" {
						t.Fatal("incompatible request accepted")
					}
				})
			}
			t.Run("malformed_request", func(t *testing.T) {
				resp, _ := invoke(t, bin, f, "", "", 0, true, nil)
				if resp.Error == "" {
					t.Fatal("malformed request accepted")
				}
			})
			if !f.RawOutput && !f.UsageUnavailable {
				t.Run("usage_unavailable", func(t *testing.T) {
					var lines []string
					for _, line := range strings.Split(f.SuccessStream, "\n") {
						var event map[string]any
						if err := json.Unmarshal([]byte(line), &event); err != nil {
							t.Fatal(err)
						}
						removeUsage(event)
						b, err := json.Marshal(event)
						if err != nil {
							t.Fatal(err)
						}
						lines = append(lines, string(b))
					}
					resp, _ := invoke(t, bin, f, strings.Join(lines, "\n"), "", 0, false, nil)
					if resp.Error != "" || resp.Usage != nil {
						t.Fatalf("missing usage: %+v", resp)
					}
				})
				t.Run("reported_zero", func(t *testing.T) {
					stream := strings.NewReplacer(":120", ":0", ":30", ":0", ":40", ":0").Replace(f.SuccessStream)
					resp, _ := invoke(t, bin, f, stream, "", 0, false, nil)
					if resp.Error != "" || resp.Usage == nil || *resp.Usage != (contract.TokenUsage{}) {
						t.Fatalf("zero usage: %+v", resp)
					}
				})
			}
			// Artifact support is verified by a real file, rather than a binary
			// presence check. Typed result schema enforcement stays opt-in.
			if manifest.Supports(contract.CapabilityArtifacts) {
				t.Run("missing_artifact", func(t *testing.T) {
					resp, _ := invoke(t, bin, f, f.SuccessStream, "", 0, false, map[string]adapter.OutputDecl{"missing": {Path: "absent.txt", Mime: "text/plain"}})
					if resp.Error == "" {
						t.Fatal("missing declared artifact accepted")
					}
				})
				t.Run("invalid_json_preserves_execution_success_and_artifact", func(t *testing.T) {
					resp, _ := invoke(t, bin, f, f.SuccessStream, "", 0, false, map[string]adapter.OutputDecl{
						"report":     {Path: "report.txt", Mime: "text/plain"},
						"report.txt": {Schema: json.RawMessage(`{"type":"string"}`)},
						"absent":     {Schema: json.RawMessage(`{"type":"boolean"}`)},
					})
					artifact, ok := resp.Outputs["report"].(map[string]any)
					if resp.Error != "" || resp.OutputErrors["report.txt"] == "" || resp.OutputErrors["absent"] == "" || resp.Outputs["report.txt"] != "report" || !ok || artifact["data"] != "report" {
						t.Fatalf("JSON result errors lost execution success or artifact: %+v", resp)
					}
				})
				t.Run("artifacts_and_scalar", func(t *testing.T) {
					resp, _ := invoke(t, bin, f, f.SuccessStream, "", 0, false, map[string]adapter.OutputDecl{"report": {Path: "report.txt", Mime: "text/plain"}, "value": {Mime: "application/json"}})
					artifact, ok := resp.Outputs["report"].(map[string]any)
					value, scalarOK := resp.Outputs["value"].(map[string]any)
					if resp.Error != "" || !ok || artifact["data"] != "report" || artifact["mime"] != "text/plain" || !scalarOK || value["ok"] != true {
						t.Fatalf("artifact/scalar result: %+v", resp)
					}
				})
			}
		})
	}
}

func testGracefulCancel(t *testing.T, bin string, f Fixture) {
	t.Helper()
	workspace := t.TempDir()
	cli := filepath.Join(workspace, f.CLIName)
	script := "#!/bin/sh\nprintf 'started\\n'\nexec /bin/sleep 30\n"
	if f.CancelScript != "" {
		script = f.CancelScript
	}
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	req := adapter.Request{Executor: f.Executor, Workspace: workspace, WorkDir: t.TempDir(), HostExecutionAllowed: true,
		RequiredCapabilities: map[string]string{contract.CapabilityCancel: contract.Version},
		Params:               json.RawMessage(fmt.Sprintf(`{"program":%q}`, cli))}
	input, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(bin, f.Binary))
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + workspace, "MUXLIO_LOG_FD=3"}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cmd.ExtraFiles = []*os.File{w}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	// Wait for actual child progress, so cancellation never races startup.
	var progress contract.Progress
	decoder := json.NewDecoder(r)
	decodeErr := decoder.Decode(&progress)
	for decodeErr == nil && f.CancelReadyText != "" && !strings.Contains(progress.Text, f.CancelReadyText) {
		decodeErr = decoder.Decode(&progress)
	}
	signalErr := cmd.Process.Signal(os.Interrupt)
	waitErr := cmd.Wait()
	if decodeErr != nil || signalErr != nil || waitErr != nil {
		t.Fatalf("cancel lifecycle: %v %v %v; %s", decodeErr, signalErr, waitErr, stderr.String())
	}
	var result contract.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !strings.Contains(result.Error, "cancelled") {
		t.Fatalf("cancel must return an error result: %s (%v)", stdout.String(), err)
	}
}

func invoke(t *testing.T, bin string, f Fixture, stream, stderr string, code int, malformed bool, outputs map[string]adapter.OutputDecl, mutate ...func(*adapter.Request)) (contract.Result, string) {
	t.Helper()
	workspace, scratch, fakeBin := t.TempDir(), t.TempDir(), t.TempDir()
	// Stub writes artifacts to the directory in the request, independent of
	// provider command arguments. Values are quoted as shell literals.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	marker := filepath.Join(scratch, "launched")
	script := "#!/bin/sh\n: > " + quote(marker) + "\nprintf '%s\\n' " + quote(stream) + "\nprintf '%s\\n' " + quote(stderr) + " >&2\n"
	script += "printf 'report' > " + quote(filepath.Join(scratch, "report.txt")) + "\nprintf '{\"ok\":true}' > " + quote(filepath.Join(scratch, "value")) + "\n"
	if code < 0 {
		script += "kill -KILL $$\n"
	} else {
		script += fmt.Sprintf("exit %d\n", code)
	}
	if f.Script != nil {
		script = "#!/bin/sh\n: > " + quote(marker) + "\n" + f.Script(stream, stderr, code, scratch)
	}
	cli := filepath.Join(fakeBin, f.CLIName)
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	req := adapter.Request{ContractVersion: contract.Version, Workspace: workspace, WorkDir: scratch, Executor: f.Executor, Task: "conformance", HostExecutionAllowed: true, Secrets: map[string]string{"key": "SUPER_SECRET_API_KEY_xyz123"}, Outputs: outputs}
	if f.RawOutput {
		req.Params = json.RawMessage(fmt.Sprintf(`{"program":%q}`, cli))
	}
	for _, fn := range mutate {
		fn(&req)
	}
	input, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if malformed {
		input = []byte("{broken")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(bin, f.Binary))
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + fakeBin, "HOME=" + scratch, "MUXLIO_LOG_FD=3"}
	log, err := os.CreateTemp(t.TempDir(), "progress")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.ExtraFiles = []*os.File{log}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process: %v\n%s\n%s", err, raw, diagnostics.String())
	}
	if len(mutate) > 0 {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("incompatible request launched provider: %v", err)
		}
	}
	if bytes.Contains(diagnostics.Bytes(), []byte("SUPER_SECRET_API_KEY_xyz123")) {
		t.Fatal("secret leaked into adapter stderr")
	}
	if bytes.Contains(raw, []byte("SUPER_SECRET_API_KEY_xyz123")) {
		t.Fatal("secret leaked into response")
	}
	result, err := contract.ParseResult(raw)
	if err != nil {
		t.Fatalf("invalid response: %v\n%s", err, raw)
	}
	progress, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(progress), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event contract.Progress
		if err := json.Unmarshal(line, &event); err != nil || event.Stream == "" {
			t.Fatalf("invalid progress: %s", line)
		}
	}
	return result, string(progress)
}

func removeUsage(event map[string]any) {
	for key, value := range event {
		if key == "usage" || key == "stats" || key == "tokenUsage" {
			delete(event, key)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			removeUsage(nested)
		}
	}
}

// Put a secret in provider text as well as stderr to test both live progress
// and terminal diagnostic outputs through the same contract.
func secretStream(t *testing.T, f Fixture) string {
	t.Helper()
	const secret = "SUPER_SECRET_API_KEY_xyz123"
	if f.RawOutput {
		return secret
	}
	var lines []string
	var rewrite func(map[string]any)
	rewrite = func(event map[string]any) {
		for key, value := range event {
			switch nested := value.(type) {
			case map[string]any:
				rewrite(nested)
			case string:
				if key == "text" || key == "result" || key == "finalText" {
					event[key] = secret
				}
			}
		}
	}
	for _, line := range strings.Split(f.SuccessStream, "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		rewrite(event)
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
	}
	return strings.Join(lines, "\n")
}
