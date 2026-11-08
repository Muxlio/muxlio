package sqlite

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestDispatchNumericMetadataBoundaries(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `{"n":[1e400,9007199254740993]}`} {
		t.Run(value, func(t *testing.T) {
			s := openTemp(t)
			raw := []byte(`{"inputs":{"value":` + value + `}}`)
			d := state.Dispatch{AssignmentID: "attempt", AttemptID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", Request: raw}
			// Probe metadata readers separately so a failing decode cannot mask another.
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			_, err = admissionReason(tx, state.AdmissionConfig{}, d, state.AdmissionDemand{Slots: 1}, "worker", 1, time.Now())
			if err != nil {
				t.Error("admission:", err)
			}
			err = reserveAdmission(tx, state.AdmissionConfig{}, d, state.AdmissionDemand{Slots: 1}, time.Now())
			if err != nil {
				t.Error("reservation:", err)
			}
			tx.Rollback()
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			if err := s.EnqueueDispatch(d); err != nil {
				t.Error("enqueue:", err)
				// Still exercise the remaining readers when enqueue regresses.
				d.Request = []byte(`{}`)
				if err := s.EnqueueDispatch(d); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE dispatches SET request_json=? WHERE assignment_id='attempt'`, string(raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExpireQueuedAttempt("attempt", time.Now()); err != nil {
				t.Error("queue timeout:", err)
			}
			_, err = s.db.Exec(`INSERT INTO assignments(assignment_id,run_id,activation_id,attempt_id,worker_id,generation,coordinator_epoch,lease_until,state) VALUES('attempt','run','act','attempt','worker',1,0,?,'active')`, rfc3339(time.Now().Add(time.Minute)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE dispatches SET state='assigned'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExpireExecutingAttempt("attempt", time.Now()); err != nil {
				t.Error("execution timeout:", err)
			}
			accepted, err := s.CompleteWorkerAssignment(state.Assignment{AssignmentID: "attempt", RunID: "run", ActivationID: "act", AttemptID: "attempt", WorkerID: "worker", Generation: 1}, "succeeded", time.Now(), []byte(`{}`))
			if err != nil || !accepted {
				t.Errorf("completion accepted=%v err=%v", accepted, err)
			}
		})
	}
}
