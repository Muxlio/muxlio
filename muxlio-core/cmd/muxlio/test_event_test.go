package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestTestEventAcceptsForwardedTokenFlags(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	payloadFile := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(secretFile, []byte("0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadFile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Flags are accepted for compatibility with global muxlio client options.
	code := cmdTestEvent([]string{
		"--token", "unused",
		"--token-file", filepath.Join(dir, "missing"),
		"--server", "http://127.0.0.1:9",
		"--secret-file", secretFile,
		"trigger", payloadFile,
	}, "")
	if code == 2 {
		t.Fatal("forwarded --token/--token-file must parse")
	}
}

func TestWebhookStartsAndExecutesPinnedRun(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	executor := workers.NewFake()
	runner := &serverRunner{store: s, exec: executor, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: context.Background()}
	router := api.NewRouter(s, runner)
	post := func(path string, raw []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewReader(raw)))
		if rec.Code != 201 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	p := &plan.Plan{Name: "hook", IRVersion: "1.0", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/task"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	post("/v1/workflow-versions", raw)
	raw, _ = json.Marshal(map[string]any{"id": "test", "workflowDigest": digest, "secret": "0123456789abcdef", "signatureEncoding": "base64", "signatureHeader": "X-Test", "signaturePrefix": "sig="})
	post("/v1/triggers", raw)
	server := httptest.NewServer(api.WithAuth(router, "admin", "worker"))
	defer server.Close()
	secretFile := filepath.Join(t.TempDir(), "secret")
	payloadFile := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(secretFile, []byte("0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadFile, []byte(`{"event":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--server", server.URL, "--secret-file", secretFile, "--idempotency-key", "one", "--signature-header", "X-Test", "--signature-prefix", "sig=", "--signature-encoding", "base64", "test", payloadFile}
	for i := 0; i < 2; i++ {
		if code := cmdTestEvent(args, ""); code != 0 {
			t.Fatalf("test-event exit %d", code)
		}
	}
	runner.wg.Wait()
	runs, err := s.ListRuns()
	if err != nil || len(runs) != 1 || runs[0].State != plan.RunSucceeded || runs[0].PlanDigest != digest || len(executor.Calls()) != 1 {
		t.Fatalf("runs=%+v calls=%d err=%v", runs, len(executor.Calls()), err)
	}
	// Simulate a crash between atomic acceptance and engine attachment.
	// Recovery must use the stored immutable plan without asking the sender
	// to redeliver or registering another workflow version.
	body := []byte(`{"event":true}`)
	pending := httptest.NewRequest("POST", "/webhooks/test", bytes.NewReader(body))
	pending.Header.Set("Idempotency-Key", "recover-me")
	signature := hmac.New(sha256.New, []byte("0123456789abcdef"))
	signature.Write(body)
	pending.Header.Set("X-Test", "sig="+base64.StdEncoding.EncodeToString(signature.Sum(nil)))
	accepted := httptest.NewRecorder()
	api.NewRouter(s, nil).ServeHTTP(accepted, pending)
	if accepted.Code != 201 {
		t.Fatalf("pending delivery: %d %s", accepted.Code, accepted.Body.String())
	}
	recovered, err := plan.RecoverRuns(s, executor)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovery: %d %v", len(recovered), err)
	}
	for id, engine := range recovered {
		if err := engine.Run(id); err != nil {
			t.Fatal(err)
		}
		run, err := s.GetRun(id)
		if err != nil || run.State != plan.RunSucceeded || run.PlanDigest != digest {
			t.Fatalf("recovered run: %+v %v", run, err)
		}
	}
	// Disabled triggers reject even correctly signed events.
	post("/v1/triggers", []byte(`{"id":"disabled","workflowDigest":"`+digest+`","secret":"0123456789abcdef","enabled":false,"signatureEncoding":"base64"}`))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/disabled", bytes.NewReader([]byte(`{}`)))
	mac := hmac.New(sha256.New, []byte("0123456789abcdef"))
	mac.Write([]byte(`{}`))
	req.Header.Set("X-Muxlio-Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("disabled trigger: %d", rec.Code)
	}
	if code := cmdTestEvent([]string{}, ""); code != 2 {
		t.Fatalf("invalid invocation exit %d", code)
	}
}
