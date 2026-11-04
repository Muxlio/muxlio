package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestSubmitFeaturesConstructsPlan(t *testing.T) {
	input := json.RawMessage(`{"features":[{"id":"one","feature":"Add a checklist","context":"Docs only"},{"id":"two","feature":"Clarify usage"}],"branch":"main"}`)
	called := false
	ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs" || r.Header.Get("Authorization") != "Bearer configured-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		var request struct {
			Plan  plan.Plan       `json:"plan"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Plan.Name != "implement-features" || request.Plan.Digest == "" {
			t.Errorf("wrong feature plan: %+v", request.Plan)
		}
		if errs := plan.Validate(&request.Plan); len(errs) != 0 {
			t.Errorf("invalid plan: %v", errs)
		}
		var gotInput, wantInput any
		_ = json.Unmarshal(request.Input, &gotInput)
		_ = json.Unmarshal(input, &wantInput)
		got, _ := json.Marshal(gotInput)
		want, _ := json.Marshal(wantInput)
		if string(got) != string(want) {
			t.Errorf("input changed: %s", got)
		}
		commit := false
		for _, graph := range request.Plan.Subgraphs {
			for _, node := range graph.Nodes {
				if node.ID == "push" {
					t.Error("feature plan must not push")
				}
				if node.ID == "commit" {
					commit = true
				}
			}
		}
		if !commit {
			t.Error("feature plan must commit")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runId":"run_features","state":"running"}`))
	}))
	s := &server{api: newAPIClient(ts.URL, "configured-token")}
	result := s.dispatchTool("submit_features", input)
	if result.IsError || !called || !strings.Contains(result.Content[0].Text, "run_features") {
		t.Fatalf("submission: %+v", result)
	}
}

func TestSubmitFeaturesRejectsInvalidInputBeforeSubmission(t *testing.T) {
	ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid feature input was submitted")
	}))
	s := &server{api: newAPIClient(ts.URL)}
	tooMany := `{"features":[` + strings.TrimSuffix(strings.Repeat(`{"id":"one","feature":"test"},`, 21), ",") + `]}`
	for _, input := range []string{
		`{}`, `null`, `{"features":[]}`, `{"features":"bad"}`, `{"features":[{"id":"one"}]}`,
		`{"features":[{"id":" ","feature":"test"}]}`, `{"features":[{"id":"one","feature":" "}]}`,
		`{"features":[{"id":"one","feature":"a"},{"id":"one","feature":"b"}]}`, tooMany,
		`{"features":[{"id":"one","feature":"test","unknown":true}]}`,
		`{"features":[{"id":"one","feature":"test"}],"unknown":true}`,
	} {
		if result := s.dispatchTool("submit_features", json.RawMessage(input)); !result.IsError {
			t.Errorf("accepted %s: %+v", input, result)
		}
	}
}

func TestRetryAndAttemptInspection(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/run_one/retry" || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("unexpected retry request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"ok":true,"runId":"run_one","action":"retry"}`))
			}))
			s := &server{api: newAPIClient(ts.URL, "token")}
			result := s.dispatchTool("retry_run", json.RawMessage(`{"runId":"run_one"}`))
			if result.IsError != (status != http.StatusOK) {
				t.Fatalf("retry error handling: %+v", result)
			}
			if !s.dispatchTool("retry_run", json.RawMessage(`{}`)).IsError {
				t.Fatal("accepted missing run ID")
			}
		})
	}
	ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run_one/attempts" || r.URL.Query().Get("activationId") != "step:one & two" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected attempts request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"attempts":[{"id":"missing","usage":null},{"id":"zero","usage":{"input":0,"output":0,"cached":0}},{"id":"reported","usage":{"input":12,"output":3,"cached":4}}]}`))
	}))
	s := &server{api: newAPIClient(ts.URL, "token")}
	result := s.dispatchTool("get_attempts", json.RawMessage(`{"runId":"run_one","activationId":"step:one & two"}`))
	if result.IsError {
		t.Fatal(result)
	}
	var got struct {
		Attempts []state.ExecutionAttempt `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 3 || got.Attempts[0].Usage != nil || got.Attempts[1].Usage == nil || *got.Attempts[1].Usage != (state.TokenUsage{}) || got.Attempts[2].Usage == nil || *got.Attempts[2].Usage != (state.TokenUsage{Input: 12, Output: 3, Cached: 4}) {
		t.Fatalf("usage availability changed: %s", result.Content[0].Text)
	}
	if !s.dispatchTool("get_attempts", json.RawMessage(`{}`)).IsError {
		t.Fatal("accepted missing run ID")
	}
}

func TestAuthenticatedArtifactTools(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, mime, body string
		maxBytes         int
		headFallback     bool
	}{
		{"text", "text/markdown", "# Notes\nDone", 64, false},
		{"text truncated", "text/plain", "hello world", 5, false},
		{"binary", "application/octet-stream", "\x00\xff\x80\x01", 64, false},
		{"binary truncated", "application/octet-stream", "\xff\xfe\xfd\xfc", 3, false},
		{"split UTF8", "text/plain", "éé", 3, false},
		{"metadata fallback", "text/plain", "hello", 64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer artifact-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path != "/v1/artifacts/"+digest {
					t.Errorf("unexpected artifact path: %s", r.URL)
				}
				if r.Method == http.MethodHead && tc.headFallback {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", tc.mime)
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				if r.Method != http.MethodHead {
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			s := &server{api: newAPIClient(ts.URL, "artifact-token")}
			args, _ := json.Marshal(map[string]any{"digest": digest, "maxBytes": tc.maxBytes})
			result := s.dispatchTool("read_artifact", args)
			if result.IsError {
				t.Fatal(result)
			}
			var got struct {
				Content   string `json:"content"`
				Encoding  string `json:"encoding"`
				Size      int    `json:"size"`
				Truncated bool   `json:"truncated"`
			}
			if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
				t.Fatal(err)
			}
			content := []byte(got.Content)
			if got.Encoding == "base64" {
				var err error
				content, err = base64.StdEncoding.DecodeString(got.Content)
				if err != nil {
					t.Fatal(err)
				}
			}
			want := tc.body
			if len(want) > tc.maxBytes {
				want = want[:tc.maxBytes]
			}
			if string(content) != want || got.Size != len(tc.body) || got.Truncated != (len(tc.body) > tc.maxBytes) {
				t.Fatalf("artifact bytes changed: %+v", got)
			}
			metadata, _ := json.Marshal(map[string]string{"digest": digest})
			result = s.dispatchTool("get_artifact_metadata", metadata)
			if result.IsError || !strings.Contains(result.Content[0].Text, digest) {
				t.Fatalf("authenticated metadata: %+v", result)
			}
			unauthorized := &server{api: newAPIClient(ts.URL)}
			for _, name := range []string{"read_artifact", "get_artifact_metadata"} {
				result := unauthorized.dispatchTool(name, metadata)
				if !result.IsError || !strings.Contains(result.Content[0].Text, "401") {
					t.Fatalf("%s should reject missing credentials: %+v", name, result)
				}
			}
		})
	}
}

func TestArtifactInputLimits(t *testing.T) {
	s := &server{api: newAPIClient("http://127.0.0.1:1")}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, args := range []string{`{}`, `{"digest":"../config"}`, `{"digest":"sha256:bad"}`, `{"digest":"` + digest + `","maxBytes":0}`, `{"digest":"` + digest + `","maxBytes":1048577}`, `{"digest":"` + digest + `","maxBytes":1.5}`} {
		result := s.dispatchTool("read_artifact", json.RawMessage(args))
		if !result.IsError || strings.Contains(result.Content[0].Text, "request failed") {
			t.Errorf("input not rejected locally: %s: %+v", args, result)
		}
	}
}

func TestCoordinatorProfilesAndCredentialRouting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(filepath.Join(dir, "remote-token"), []byte(" file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"defaultServer":"remote","token":"local-token","servers":{"remote":{"url":"https://remote.example/","token":"remote-token"},"file":{"url":"https://file.example","tokenFile":"remote-token"},"string":"https://string.example"},"profiles":{"legacy":{"url":"https://legacy.example","token":"legacy-token"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUXLIO_CONFIG", path)
	t.Setenv("MUXLIO_SERVER", "")
	t.Setenv("MUXLIO_TOKEN", "")
	for _, tc := range []struct{ target, url, token string }{
		{"", "https://remote.example", "remote-token"}, {"remote", "https://remote.example", "remote-token"},
		{"https://remote.example", "https://remote.example", ""}, {"file", "https://file.example", "file-token"},
		{"legacy", "https://legacy.example", "legacy-token"}, {"string", "https://string.example", "local-token"},
		{"http://127.0.0.1:7600", "http://127.0.0.1:7600", ""},
		{"https://unrelated.example", "https://unrelated.example", ""}, {"http://127.0.0.1:9999", "http://127.0.0.1:9999", ""},
	} {
		url, token, err := resolveCoordinator(tc.target)
		if err != nil || url != tc.url || token != tc.token {
			t.Errorf("target %q: url=%s token matches=%v err=%v", tc.target, url, token == tc.token, err)
		}
	}
	t.Setenv("MUXLIO_SERVER", "legacy")
	url, _, err := resolveCoordinator("")
	if err != nil || url != "https://legacy.example" {
		t.Fatalf("environment target ignored: %s %v", url, err)
	}
	t.Setenv("MUXLIO_TOKEN", "override")
	_, token, err := resolveCoordinator("remote")
	if err != nil || token != "override" {
		t.Fatal("environment credential override ignored")
	}
	for _, target := range []string{"missing", "file:///etc/passwd", "https://user:password@example.com", "https://remote.example?query=1"} {
		if _, _, err := resolveCoordinator(target); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
}

func TestCoordinatorRequiresConfigurationWithoutLocalInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MUXLIO_CONFIG", path)
	t.Setenv("MUXLIO_SERVER", "")
	t.Setenv("MUXLIO_TOKEN", "")
	write := func(config string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"addr":"0.0.0.0:7700","token":"saved-token"}`)
	if _, _, err := resolveCoordinator(""); err == nil {
		t.Fatal("silently defaulted to a local coordinator")
	}
	for _, target := range []string{"http://localhost:7700", "http://127.0.0.1:7700", "http://127.0.0.1:7600", "https://remote.example:7700"} {
		_, token, err := resolveCoordinator(target)
		if err != nil || token != "" {
			t.Fatalf("inferred credentials for %s: err=%v", target, err)
		}
	}
	for _, target := range []string{"http://localhost:7700", "https://remote.example:7700"} {
		raw, _ := json.Marshal(map[string]string{"defaultServer": target, "token": "saved-token"})
		write(string(raw))
		gotURL, token, err := resolveCoordinator("")
		if err != nil || gotURL != target || token != "saved-token" {
			t.Fatalf("configured URL not used: %s token present=%v err=%v", gotURL, token != "", err)
		}
	}
	write(`{"servers":{"selected":{"url":"https://remote.example","token":"profile-token"},"unused":{"url":123}}}`)
	_, token, err := resolveCoordinator("selected")
	if err != nil || token != "profile-token" {
		t.Fatal("unrelated profile interfered with selected coordinator")
	}
	t.Setenv("MUXLIO_TOKEN", "explicit-token")
	_, token, err = resolveCoordinator("http://localhost:7700")
	if err != nil || token != "explicit-token" {
		t.Fatal("explicit URL credentials not used")
	}
}
