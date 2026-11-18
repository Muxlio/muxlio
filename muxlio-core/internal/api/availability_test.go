package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestAvailabilityRecheckRejectsQuotaNoOpClear(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.MarkExecutorUnavailable(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"}, contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerAvailability(mux, store)
	body, _ := json.Marshal(map[string]string{"workerId": "w1", "executor": "agent/x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/executors/availability/recheck", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	blocked, err := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"})
	if err != nil || blocked == nil {
		t.Fatalf("quota block must remain: %+v %v", blocked, err)
	}
}

func TestAvailabilityRecheckClearsAuth(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.MarkExecutorUnavailable(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"}, contract.Availability{
		Reason: contract.AvailabilityAuthenticationFailed, Message: "auth",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerAvailability(mux, store)
	body, _ := json.Marshal(map[string]string{"workerId": "w1", "executor": "agent/x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/executors/availability/recheck", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	blocked, err := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"})
	if err != nil || blocked != nil {
		t.Fatalf("auth recheck should clear: %+v %v", blocked, err)
	}
}
