package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestCompletionAndQueuedExactNumbers(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(value, func(t *testing.T) {
			s, err := sqlite.Open(filepath.Join(t.TempDir(), "numbers.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			c := NewCoordinator(s)
			if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"executor":"test","inputs":{"value":` + value + `}}`)
			if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "attempt", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
				t.Fatal(err)
			}
			// Session and artifact callers also read the complete generic request.
			req, err := c.assignmentRequest("attempt")
			if err != nil || req == nil {
				t.Fatalf("assignment request=%+v err=%v", req, err)
			} else {
				got, _ := json.Marshal(req.Inputs["value"])
				if string(got) != value {
					t.Errorf("assignment=%s", got)
				}
			}
			a, err := c.NextAssignment("worker")
			if err != nil || a == nil {
				t.Fatalf("assignment=%v err=%v", a, err)
			}
			body := `{"outputs":{"value":` + value + `}}`
			var completion WorkerCompletion
			if !decodeWorkerJSON(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &completion) {
				t.Fatal("completion decode rejected")
			}
			got, _ := json.Marshal(completion.Outputs["value"])
			if string(got) != value {
				t.Errorf("completion=%s", got)
			}
			completion.AssignmentID = a.AssignmentID
			completion.AttemptID = a.AttemptID
			completion.RunID = a.RunID
			completion.ActivationID = a.ActivationID
			completion.CoordinatorEpoch = a.CoordinatorEpoch
			completion.Generation = a.Generation
			completion.State = "succeeded"
			if err := c.Complete("worker", completion); err != nil {
				t.Fatal(err)
			}
			r, err := NewCoordinator(s).Execute(workers.ExecuteRequest{Context: context.Background(), Executor: "test", RunID: "run", ActivationID: "activation", AttemptID: "attempt"})
			got, _ = json.Marshal(r.Outputs["value"])
			if err != nil || string(got) != value {
				t.Errorf("queued result=%s err=%v", got, err)
			}
			for _, suffix := range []string{" {}", " trailing", strings.Repeat(" ", 4<<20)} {
				if decodeWorkerJSON(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body+suffix)), &completion) {
					t.Error("accepted trailing/oversized JSON")
				}
			}
		})
	}
}
