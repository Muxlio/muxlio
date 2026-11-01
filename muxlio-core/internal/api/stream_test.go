package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestEventStreaming(t *testing.T) {
	st := state.New()
	run := &state.Run{
		ID:           "run_stream_test",
		WorkflowName: "stream_test",
		State:        "running",
	}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	_ = st.AppendEvent("run_stream_test", "run.running", "run_stream_test", map[string]any{"state": "running"})

	router := NewRouter(st, nil)
	srv := httptest.NewServer(router)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, endpoint := range []string{
		fmt.Sprintf("%s/v1/runs/run_stream_test/events/stream", srv.URL),
		fmt.Sprintf("%s/v1/runs/run_stream_test/stream", srv.URL),
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("endpoint %s failed: %v", endpoint, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("endpoint %s returned status %d", endpoint, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("endpoint %s unexpected Content-Type: %s", endpoint, ct)
		}

		reader := bufio.NewReader(resp.Body)
		foundEvent := false
		for i := 0; i < 10; i++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if strings.HasPrefix(line, "event: run.running") {
				foundEvent = true
				break
			}
		}
		resp.Body.Close()
		if !foundEvent {
			t.Errorf("endpoint %s did not receive run.running event", endpoint)
		}
	}
}
