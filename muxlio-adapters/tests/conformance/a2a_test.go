package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/conformancetest"
	"github.com/muxlio/muxlio-adapters/contract"
)

func a2aFixture() conformancetest.Fixture {
	return conformancetest.Fixture{Executor: "agent/a2a", Binary: "agent-a2a", UsageUnavailable: true, Lifecycle: func(t *testing.T, binary string, m *contract.AdapterManifest) {
		var endpoint string
		var submitted atomic.Int32
		var completed atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "0.3.0", "url": endpoint, "defaultInputModes": []string{"text/plain"}})
				return
			}
			var request struct {
				Method string `json:"method"`
				Params struct {
					ID string `json:"id"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if request.Method == "message/send" {
				submitted.Add(1)
			} else if request.Method != "tasks/get" || request.Params.ID != "remote-1" {
				t.Errorf("unexpected remote lookup: %+v", request)
			}
			state := "working"
			if completed.Load() {
				state = "completed"
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "result": map[string]any{
				"kind": "task", "id": "remote-1", "contextId": "context-1", "status": map[string]any{"state": state},
				"artifacts": []any{map[string]any{"name": "value", "parts": []any{map[string]any{"kind": "data", "data": true}}}, map[string]any{"name": "report", "parts": []any{map[string]any{"kind": "file", "file": map[string]any{"bytes": "AP8="}}}}},
			}})
		}))
		defer server.Close()
		endpoint = server.URL + "/rpc"
		params, _ := json.Marshal(map[string]any{"url": server.URL})
		req := contract.Request{Executor: m.Executor, Params: params, ContractVersion: contract.Version, WorkDir: t.TempDir(), Outputs: map[string]contract.OutputDecl{"value": {}, "report": {Path: "report.bin", Mime: "application/octet-stream"}}, RequiredCapabilities: map[string]string{contract.CapabilityRemoteTask: contract.Version}}
		invokeRemote := func(req contract.Request) (contract.Result, []contract.Progress) {
			t.Helper()
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary)
			cmd.Stdin = bytes.NewReader(raw)
			log, err := os.CreateTemp(t.TempDir(), "progress")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.ExtraFiles = []*os.File{log}
			cmd.Env = append(os.Environ(), "MUXLIO_LOG_FD=3")
			raw, err = cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			result, err := contract.ParseResult(raw)
			if err != nil {
				t.Fatalf("invalid result: %s: %v", raw, err)
			}
			raw, err = os.ReadFile(log.Name())
			if err != nil {
				t.Fatal(err)
			}
			var progress []contract.Progress
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var p contract.Progress
				if err := json.Unmarshal(line, &p); err != nil {
					t.Fatal(err)
				}
				progress = append(progress, p)
			}
			return result, progress
		}
		pending, progress := invokeRemote(req)
		if pending.Error != "" || pending.RemoteTask == nil || !pending.RemoteTask.Pending || pending.RemoteTask.ContextID != "context-1" || pending.Usage != nil {
			t.Fatalf("handoff: %+v", pending)
		}
		if len(progress) != 1 || progress[0].OperationRef == nil || progress[0].OperationRef.ID != "remote-1" {
			t.Fatalf("durable progress missing: %+v", progress)
		}
		req.RemoteTask = pending.RemoteTask
		req.Action = "poll"
		completed.Store(true)
		done, _ := invokeRemote(req)
		if done.Error != "" || done.RemoteTask.Pending || done.Outputs["value"] != true || done.Usage != nil {
			t.Fatalf("resumed completion: %+v", done)
		}
		data, err := os.ReadFile(done.Outputs["report"].(map[string]any)["path"].(string))
		if err != nil || !bytes.Equal(data, []byte{0, 255}) {
			t.Fatalf("binary artifact corrupted: %v %v", data, err)
		}
		req.RemoteTask = nil
		req.Action = "reconcile"
		unknown, _ := invokeRemote(req)
		if unknown.Reconciliation == nil || unknown.Reconciliation.Status != "inconclusive" {
			t.Fatalf("uncertain submission: %+v", unknown)
		}
		req.Action = ""
		req.RequiredCapabilities = map[string]string{contract.CapabilityIdempotencyKey: contract.Version}
		rejected, _ := invokeRemote(req)
		if !strings.Contains(rejected.Error, contract.CapabilityIdempotencyKey) || submitted.Load() != 1 {
			t.Fatalf("unsupported capability repeated remote work: %+v", rejected)
		}
	}}
}
