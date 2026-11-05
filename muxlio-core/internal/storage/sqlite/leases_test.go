package sqlite

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestLeaseRecoveryFencesReplayAndHoldsUnsafeEffects(t *testing.T) {
	for _, loss := range []string{"expiry", "coordinator-restart", "worker-restart"} {
		t.Run(loss, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			epoch, err := s.StartCoordinator()
			if err != nil {
				t.Fatal(err)
			}
			admissionWorker(t, s, "worker", 2)
			if err := s.ConfigureAdmission(state.AdmissionConfig{GlobalCap: 2}); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"pure", "unsafe"} {
				raw := []byte(`{"effect":"none"}`)
				if id == "unsafe" {
					raw = []byte(`{}`)
				}
				if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: id, AttemptID: id, ActivationID: id, RunID: "run", Executor: "test", Request: raw}); err != nil {
					t.Fatal(err)
				}
			}
			old := make(map[string]state.Assignment)
			for i := 0; i < 2; i++ {
				d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
				if err != nil || d == nil {
					t.Fatalf("claim: %v %v", d, err)
				}
				old[d.AssignmentID] = state.Assignment{CoordinatorEpoch: epoch, AssignmentID: d.AssignmentID, AttemptID: d.AttemptID, ActivationID: d.ActivationID, RunID: d.RunID, WorkerID: "worker", Generation: g}
			}
			switch loss {
			case "expiry":
				if _, err := s.db.Exec(`UPDATE assignments SET lease_until=?`, rfc3339(time.Now().Add(-time.Second))); err != nil {
					t.Fatal(err)
				}
				if err := s.RecoverWorkerLeases(); err != nil {
					t.Fatal(err)
				}
			case "coordinator-restart":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				epoch, err = s.StartCoordinator()
				if err != nil {
					t.Fatal(err)
				}
			case "worker-restart":
				if err := s.RegisterWorker(state.Worker{ID: "worker", InstanceID: "replacement", Capacity: 2, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			// The lost reservation no longer consumes a global slot. Pure work is
			// eligible immediately, whereas unknown external effects remain held.
			var reserved int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM admission_reservations`).Scan(&reserved); err != nil || reserved != 0 {
				t.Fatalf("reservations=%d err=%v", reserved, err)
			}
			d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
			if err != nil || d == nil || d.AssignmentID != "pure" || g <= old["pure"].Generation {
				t.Fatalf("pure replay: %v %d %v", d, g, err)
			}
			fresh := old["pure"]
			fresh.Generation = g
			fresh.CoordinatorEpoch = epoch
			result := []byte(`{"outputs":{"value":"new"}}`)
			if ok, err := s.CompleteWorkerAssignment(fresh, "succeeded", time.Now(), result); err != nil || !ok {
				t.Fatalf("fresh completion: %v %v", ok, err)
			}
			// Both stale completion and progress must fail even after a newer result
			// was durably committed under the same assignment and attempt identifiers.
			for _, a := range old {
				if ok, _ := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), []byte(`{"stale":true}`)); ok {
					t.Fatal("accepted stale completion")
				}
				if ok, _ := s.AppendWorkerLogChunk(a, state.LogChunk{RunID: "run", ActivationID: a.ActivationID, Stream: "test", Seq: 1, Data: []byte("stale"), Timestamp: time.Now()}); ok {
					t.Fatal("accepted stale log")
				}
				if ok, err := s.AckWorkerCompletionWithoutResult(a, "cancelled", a.Generation); err != nil {
					t.Fatal(err)
				} else if ok && a.ActivationID == "unsafe" {
					t.Fatal("stale acknowledgement reconciled unsafe work")
				}
			}
			if d, _, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch); err != nil || d != nil {
				t.Fatalf("unsafe effect replayed: %v %v", d, err)
			}
			a, err := s.LookupWorkerAssignment("worker", "unsafe")
			if err != nil || a == nil || a.State != "uncertain" {
				t.Fatalf("unsafe outcome lost: %v %v", a, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartCoordinator(); err != nil {
				t.Fatal(err)
			}
			saved, err := s.WorkerResult("pure")
			if err != nil || !bytes.Equal(saved, result) {
				t.Fatalf("committed result lost: %s %v", saved, err)
			}
			a, err = s.LookupWorkerAssignment("worker", "unsafe")
			if err != nil || a.State != "uncertain" {
				t.Fatalf("hold lost on reopen: %v %v", a, err)
			}
		})
	}
}

func TestHeartbeatRenewsOnlyCurrentSlotEpochs(t *testing.T) {
	s := openTemp(t)
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "worker", 3)
	admissionRun(t, s, "run", 3)
	var leases []state.AssignmentLease
	for i := 0; i < 3; i++ {
		d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Minute), epoch)
		if err != nil || d == nil {
			t.Fatalf("claim: %v %v", d, err)
		}
		leases = append(leases, state.AssignmentLease{AssignmentID: d.AssignmentID, Generation: g, CoordinatorEpoch: epoch})
	}
	// A wrong generation and a wrong coordinator epoch cannot renew siblings.
	valid := leases[0]
	leases[1].Generation++
	leases[2].CoordinatorEpoch++
	until := time.Now().Add(time.Hour)
	if err := s.RenewWorkerLeases("worker", leases, time.Now(), until); err != nil {
		t.Fatal(err)
	}
	for _, lease := range leases {
		a, err := s.LookupWorkerAssignment("worker", lease.AssignmentID)
		if err != nil {
			t.Fatal(err)
		}
		renewed := a.LeaseUntil.Equal(until)
		if renewed != (lease == valid) {
			t.Fatalf("wrong slot renewal: %+v", a)
		}
	}
	if _, err := s.db.Exec(`UPDATE assignments SET lease_until=? WHERE assignment_id=?`, rfc3339(time.Now().Add(-time.Second)), valid.AssignmentID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewWorkerLeases("worker", []state.AssignmentLease{valid}, time.Now(), until); err != nil {
		t.Fatal(err)
	}
	a, err := s.LookupWorkerAssignment("worker", valid.AssignmentID)
	if err != nil || a.State != "uncertain" || a.LeaseUntil.Equal(until) {
		t.Fatalf("expired slot resurrected: %v %v", a, err)
	}
}

func TestDrainingWorkerPersistsAndFinishesExistingAssignments(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "run", 2)
	d, g := admissionClaim(t, s, "worker")
	w, err := s.ListWorkers(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w[0].Draining = true
	if err := s.RegisterWorker(w[0]); err != nil {
		t.Fatal(err)
	}
	admissionComplete(t, s, "worker", d, g)
	if next, _ := admissionClaim(t, s, "worker"); next != nil {
		t.Fatal("draining worker admitted work")
	}
	w, err = s.ListWorkers(time.Time{})
	if err != nil || !w[0].Draining {
		t.Fatalf("drain not persisted: %v %v", w, err)
	}
}

func TestSupersededCoordinatorCannotClaimOrPublish(t *testing.T) {
	s := openTemp(t)
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "run", 1)
	if _, err := s.StartCoordinator(); err != nil {
		t.Fatal(err)
	}
	if d, _, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch); err == nil || d != nil {
		t.Fatalf("old coordinator claimed work: %v %v", d, err)
	}
}

func TestExpiredLeaseCannotPublishWithOldTimestamp(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	a := state.Assignment{RunID: "run", ActivationID: "act", WorkerID: "worker", AssignmentID: "slot", AttemptID: "attempt", LeaseUntil: past.Add(time.Second)}
	g, err := s.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Generation = g
	if ok, err := s.AppendWorkerLogChunk(a, state.LogChunk{RunID: "run", ActivationID: "act", Stream: "test", Seq: 1, Data: []byte("late"), Timestamp: past}); err != nil || ok {
		t.Fatalf("expired log accepted: %v %v", ok, err)
	}
	if ok, err := s.CompleteWorkerAssignment(a, "succeeded", past, []byte(`{}`)); err != nil || ok {
		t.Fatalf("expired completion accepted: %v %v", ok, err)
	}
}
