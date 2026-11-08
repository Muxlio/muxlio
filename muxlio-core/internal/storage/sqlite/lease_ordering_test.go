package sqlite

import (
	"fmt"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

// Exercise the real storage methods at exact nanosecond boundaries. The fake
// clock also covers methods that obtain their own storage admission time.
func TestLeaseTimestampOrdering(t *testing.T) {
	cases := []struct {
		name       string
		lease, now time.Duration
	}{
		{"whole_expired", 0, 100 * time.Millisecond},
		{"whole_live", 100 * time.Millisecond, 0},
		{"prefix_expired", 100 * time.Millisecond, 110 * time.Millisecond},
		{"prefix_live", 110 * time.Millisecond, 100 * time.Millisecond},
		{"equal_whole", 0, 0},
		{"equal_fraction", 100 * time.Millisecond, 100 * time.Millisecond},
		{"nanosecond_expired", 100 * time.Millisecond, 100*time.Millisecond + time.Nanosecond},
		{"nanosecond_live", 100*time.Millisecond + time.Nanosecond, 100 * time.Millisecond},
		{"cross_second", 0, time.Second},
	}
	for _, tc := range cases {
		for _, legacy := range []bool{false, true} {
			for _, operation := range []string{"completion", "renewal", "progress", "recovery", "pure_recovery"} {
				t.Run(fmt.Sprintf("%s/legacy=%t/%s", tc.name, legacy, operation), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						path := filepath.Join(t.TempDir(), "leases.db")
						s, err := Open(path)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { s.Close() }()
						admissionWorker(t, s, "worker", 1)
						if err := s.ConfigureAdmission(state.AdmissionConfig{GlobalCap: 1}); err != nil {
							t.Fatal(err)
						}
						if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
							t.Fatal(err)
						}
						request := []byte(`{}`)
						if operation == "pure_recovery" {
							request = []byte(`{"effect":"none"}`)
						}
						if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "slot", AttemptID: "attempt", ActivationID: "act", RunID: "run", Executor: "test", Request: request}); err != nil {
							t.Fatal(err)
						}
						base := time.Now().Add(time.Second)
						deadline := base.Add(tc.lease)
						d, g, err := s.ClaimDispatch("worker", deadline)
						if err != nil || d == nil {
							t.Fatalf("claim: %v %v", d, err)
						}
						a := state.Assignment{AssignmentID: d.AssignmentID, AttemptID: d.AttemptID, ActivationID: d.ActivationID, RunID: d.RunID, WorkerID: "worker", Generation: g, LeaseUntil: deadline}
						if legacy {
							// Seed the exact representation written by older versions, then reopen
							// without changing the coordinator epoch or recovering the assignment.
							if _, err := s.db.Exec(`UPDATE assignments SET lease_until=?`, deadline.UTC().Format(time.RFC3339Nano)); err != nil {
								t.Fatal(err)
							}
							if err := s.Close(); err != nil {
								t.Fatal(err)
							}
							s, err = Open(path)
							if err != nil {
								t.Fatal(err)
							}
						}
						time.Sleep(time.Second + tc.now)
						live := tc.lease > tc.now
						switch operation {
						case "completion":
							ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), []byte(`{"outputs":{}}`))
							if err != nil || ok != live {
								t.Errorf("completion=%v want %v err=%v", ok, live, err)
							}
							saved, err := s.WorkerResult(a.AttemptID)
							if err != nil || (len(saved) > 0) != live {
								t.Errorf("durable result=%s live=%v err=%v", saved, live, err)
							}
						case "renewal":
							until := base.Add(time.Hour)
							err := s.RenewWorkerLeases("worker", []state.AssignmentLease{{AssignmentID: a.AssignmentID, Generation: g}}, time.Now(), until)
							if err != nil {
								t.Fatal(err)
							}
							got, err := s.LookupWorkerAssignment("worker", "slot")
							if err != nil || got == nil {
								t.Fatalf("lookup: %v %v", got, err)
							}
							if got.LeaseUntil.Equal(until) != live {
								t.Errorf("renewed=%v want %v", got.LeaseUntil, live)
							}
							if !live && got.State != "uncertain" {
								t.Errorf("expired state=%s", got.State)
							}
						case "progress":
							ok, err := s.AppendWorkerLogChunk(a, state.LogChunk{RunID: "run", ActivationID: "act", Stream: "test", Seq: 1, Data: []byte("progress"), Timestamp: time.Now()})
							if err != nil || ok != live {
								t.Errorf("progress=%v want %v err=%v", ok, live, err)
							}
							var count int
							if err := s.db.QueryRow(`SELECT count(*) FROM log_chunks`).Scan(&count); err != nil {
								t.Fatal(err)
							}
							if (count > 0) != live {
								t.Errorf("persisted progress=%d live=%v", count, live)
							}
						default:
							if err := s.RecoverWorkerLeases(); err != nil {
								t.Fatal(err)
							}
							got, err := s.LookupWorkerAssignment("worker", "slot")
							if err != nil || got == nil {
								t.Fatalf("lookup: %v %v", got, err)
							}
							wantAssignment, wantDispatch := "active", "assigned"
							if !live {
								wantAssignment, wantDispatch = "uncertain", "uncertain"
								if operation == "pure_recovery" {
									wantAssignment, wantDispatch = "cancelled", "pending"
								}
							}
							var dispatch string
							if err := s.db.QueryRow(`SELECT state FROM dispatches`).Scan(&dispatch); err != nil {
								t.Fatal(err)
							}
							if got.State != wantAssignment || dispatch != wantDispatch {
								t.Errorf("assignment/dispatch=%s/%s want %s/%s", got.State, dispatch, wantAssignment, wantDispatch)
							}
							var count int
							if err := s.db.QueryRow(`SELECT count(*) FROM admission_reservations`).Scan(&count); err != nil {
								t.Fatal(err)
							}
							if (count == 1) != live {
								t.Errorf("reservations=%d live=%v", count, live)
							}
							if !live && operation == "pure_recovery" {
								next, newGeneration, err := s.ClaimDispatch("worker", time.Now().Add(time.Hour))
								if err != nil || next == nil || newGeneration <= g {
									t.Fatalf("reclaim: %v generation=%d old=%d err=%v", next, newGeneration, g, err)
								}
								if ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), []byte(`{}`)); err != nil || ok {
									t.Errorf("stale completion=%v err=%v", ok, err)
								}
							}
						}
					})
				})
			}
		}
	}
}

func TestLeaseTimestampUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	// Mixed persisted precision, including a row already in the new format.
	values := []string{
		"2099-01-01T00:00:00Z",
		"2099-01-01T00:00:00.1Z",
		"2099-01-01T00:00:00.11Z",
		"2099-01-01T00:00:00.100000001Z",
		"2099-01-01T00:00:00.000000000Z",
	}
	for i, raw := range values {
		deadline, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprint(i)
		if _, err := s.ClaimAssignment(state.Assignment{RunID: "run", ActivationID: id, AssignmentID: id, AttemptID: id, WorkerID: "worker", LeaseUntil: deadline}); err != nil {
			t.Fatal(err)
		}
		if i < len(values)-1 {
			if _, err := s.db.Exec(`UPDATE assignments SET lease_until=? WHERE activation_id=?`, raw, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for reopen := 0; reopen < 2; reopen++ {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, raw := range values {
			id := fmt.Sprint(i)
			deadline, _ := time.Parse(time.RFC3339Nano, raw)
			var stored string
			if err := s.db.QueryRow(`SELECT lease_until FROM assignments WHERE activation_id=?`, id).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if want := deadline.UTC().Format("2006-01-02T15:04:05.000000000Z"); stored != want {
				t.Errorf("reopen %d: timestamp=%q want %q", reopen, stored, want)
			}
			a, err := s.LookupWorkerAssignment("worker", id)
			if err != nil || a == nil {
				t.Fatalf("lookup: %v %v", a, err)
			}
			if !a.LeaseUntil.Equal(deadline) || a.Generation != 1 || a.State != "active" || a.AttemptID != id || a.ActivationID != id || a.RunID != "run" {
				t.Errorf("migration changed assignment: %+v", a)
			}
		}
	}
}
