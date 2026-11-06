package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSignalInputRequests(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			if r.URL.Path != "/v1/runs/r/input-requests" {
				t.Error(r.URL.Path)
			}
		} else {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/v1/runs/r/input-requests/request" || r.Header.Get("Idempotency-Key") != "delivery" || string(body) != "false" {
				t.Errorf("delivery: %s %s %s", r.URL.Path, r.Header.Get("Idempotency-Key"), body)
			}
		}
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer s.Close()
	for _, args := range [][]string{
		{"r", "--list-requests", "--server", s.URL},
		{"r", "--request-id", "request", "--idempotency-key", "delivery", "--data", "false", "--server", s.URL},
	} {
		if code := cmdSignal(args); code != 0 {
			t.Fatalf("exit %d for %v", code, args)
		}
	}
	if code := cmdSignal([]string{"r", "--request-id", "request", "--server", s.URL}); code != 2 {
		t.Fatalf("missing delivery key exit=%d", code)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
