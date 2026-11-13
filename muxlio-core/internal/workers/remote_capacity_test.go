package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteWorkerLargeCapacityDoesNotPreallocateSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	capacity := int(^uint(0) >> 1)
	heartbeatSeen := false
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(req.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "heartbeat"):
			var body remoteHeartbeat
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Capacity == nil || *body.Capacity != capacity || body.AvailableSlots != capacity {
				t.Errorf("large-capacity heartbeat: %+v", body)
			}
			heartbeatSeen = true
			cancel()
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return w.Result(), nil
	})}
	if err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: capacity, HTTPClient: client}, NewFake()); err != nil {
		t.Fatal(err)
	}
	if !heartbeatSeen {
		t.Fatal("worker did not heartbeat before timeout")
	}
}

func TestRemoteWorkerInitialZeroCapacityDoesNotPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registered := make(chan int, 1)
	heartbeats := make(chan remoteHeartbeat, 8)
	var polls atomic.Int32
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(req.URL.Path, "register"):
			var body remoteRegistration
			_ = json.NewDecoder(req.Body).Decode(&body)
			registered <- body.Capacity
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(req.URL.Path, "heartbeat"):
			var body remoteHeartbeat
			_ = json.NewDecoder(req.Body).Decode(&body)
			select {
			case heartbeats <- body:
			default:
			}
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		default:
			polls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
		return w.Result(), nil
	})}
	done := make(chan error, 1)
	go func() {
		done <- RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", Capacity: 0, CapacityLimit: func() int { return 0 }, HTTPClient: client, PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond}, NewFake())
	}()
	defer func() { cancel(); <-done }()
	select {
	case capacity := <-registered:
		if capacity != 0 {
			t.Fatalf("registered capacity=%d, want zero", capacity)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not register")
	}
	for i := 0; i < 3; i++ {
		select {
		case hb := <-heartbeats:
			if hb.Capacity == nil || *hb.Capacity != 0 || hb.AvailableSlots != 0 {
				t.Fatalf("zero-capacity heartbeat: %+v", hb)
			}
		case <-time.After(time.Second):
			t.Fatal("zero-capacity worker stopped heartbeating")
		}
	}
	if polls.Load() != 0 {
		t.Fatalf("zero-capacity worker polled %d times", polls.Load())
	}
}
