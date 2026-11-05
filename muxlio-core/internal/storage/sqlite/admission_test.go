package sqlite

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func admissionWorker(t *testing.T, s *Store, id string, capacity int) {
	t.Helper()
	if err := s.RegisterWorker(state.Worker{ID: id, Capacity: capacity, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func admissionRun(t *testing.T, s *Store, id string, children int) {
	t.Helper()
	if err := s.CreateRun(&state.Run{ID: id}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < children; i++ {
		key := fmt.Sprintf("%s-%d", id, i)
		if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: key, ActivationID: key, AttemptID: key, RunID: id, Executor: "test", Request: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
}

func admissionClaim(t *testing.T, s *Store, worker string) (*state.Dispatch, int64) {
	t.Helper()
	d, g, err := s.ClaimDispatch(worker, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return d, g
}

func admissionComplete(t *testing.T, s *Store, worker string, d *state.Dispatch, g int64) {
	t.Helper()
	ok, err := s.CompleteWorkerAssignment(state.Assignment{AssignmentID: d.AssignmentID, AttemptID: d.AttemptID, ActivationID: d.ActivationID, RunID: d.RunID, WorkerID: worker, Generation: g}, "succeeded", time.Now(), []byte(`{}`))
	if err != nil || !ok {
		t.Fatalf("completion: %v %v", ok, err)
	}
}

func TestAdmissionFanoutFairnessAtCapacity(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "fanout", 1000)
	d, g := admissionClaim(t, s, "worker")
	admissionRun(t, s, "short", 1)
	if blocked, _ := admissionClaim(t, s, "worker"); blocked != nil {
		t.Fatal("exceeded worker capacity")
	}
	admissionComplete(t, s, "worker", d, g)
	d, _ = admissionClaim(t, s, "worker")
	if d == nil || d.RunID != "short" {
		t.Fatalf("short run starved behind fanout: %+v", d)
	}
}

func TestAdmissionHardLimitsAcrossConcurrentWorkers(t *testing.T) {
	for _, scope := range []string{"worker", "global", "run", "executor", "key", "provider"} {
		t.Run(scope, func(t *testing.T) {
			s := openTemp(t)
			cfg := state.AdmissionConfig{Executors: map[string]state.AdmissionDemand{"test": {Slots: 2, OperatorKey: "tenant", ProviderKey: "external"}}}
			switch scope {
			case "global":
				cfg.GlobalCap = 3
			case "run":
				cfg.PerRunCap = 3
			case "executor":
				cfg.ExecutorCaps = map[string]int{"test": 3}
			case "key":
				cfg.KeyCaps = map[string]int{"tenant": 3}
			case "provider":
				cfg.ProviderCaps = map[string]int{"external": 3}
			}
			if err := s.ConfigureAdmission(cfg); err != nil {
				t.Fatal(err)
			}
			admissionRun(t, s, "run", 12)
			for i := 0; i < 12; i++ {
				cap := 100
				if scope == "worker" {
					cap = 3
				}
				admissionWorker(t, s, fmt.Sprint(i), cap)
			}
			var wg sync.WaitGroup
			results := make(chan *state.Dispatch, 12)
			errors := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					worker := fmt.Sprint(i)
					if scope == "worker" {
						worker = "0"
					}
					d, _, err := s.ClaimDispatch(worker, time.Now().Add(time.Hour))
					results <- d
					errors <- err
				}(i)
			}
			wg.Wait()
			close(results)
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatal(err)
				}
			}
			count := 0
			for d := range results {
				if d != nil {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("admitted %d weighted requests, want 1 under cap 3", count)
			}
			var reason string
			if err := s.db.QueryRow(`SELECT reason FROM admission_queue WHERE reason<>'' LIMIT 1`).Scan(&reason); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(reason, scope) {
				t.Fatalf("queue reason %q does not explain %s", reason, scope)
			}
		})
	}
}

func TestAdmissionProviderRateAcrossWorkersAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "one", 10)
	admissionWorker(t, s, "two", 10)
	cfg := state.AdmissionConfig{ProviderRates: map[string]state.AdmissionRate{"shared": {Burst: 1, PerSecond: 0.001}}, Executors: map[string]state.AdmissionDemand{"test": {ProviderKey: "shared"}}}
	if err := s.ConfigureAdmission(cfg); err != nil {
		t.Fatal(err)
	}
	admissionRun(t, s, "run", 3)
	d, g := admissionClaim(t, s, "one")
	if d == nil {
		t.Fatal("burst not available")
	}
	admissionComplete(t, s, "one", d, g)
	if d, _ := admissionClaim(t, s, "two"); d != nil {
		t.Fatal("completion refunded a rate token")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if d, _ := admissionClaim(t, s, "two"); d != nil {
		t.Fatal("restart reset provider bucket")
	}
	if _, err := s.db.Exec(`UPDATE admission_buckets SET updated_at=?`, rfc3339(time.Now().Add(-2000*time.Second))); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "two"); d == nil {
		t.Fatal("provider bucket did not refill")
	}
}

func TestAdmissionBlockedRunDoesNotBlockPeerOrConsumeRate(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 10)
	cfg := state.AdmissionConfig{PerRunCap: 1, ProviderRates: map[string]state.AdmissionRate{"shared": {Burst: 2, PerSecond: 0.001}}, Executors: map[string]state.AdmissionDemand{"test": {ProviderKey: "shared"}}}
	if err := s.ConfigureAdmission(cfg); err != nil {
		t.Fatal(err)
	}
	admissionRun(t, s, "first", 3)
	if d, _ := admissionClaim(t, s, "worker"); d == nil {
		t.Fatal("first dispatch blocked")
	}
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatal("run cap exceeded")
	}
	admissionRun(t, s, "peer", 1)
	if d, _ := admissionClaim(t, s, "worker"); d == nil || d.RunID != "peer" {
		t.Fatal("blocked run consumed rate or prevented peer admission")
	}
}

func TestAdmissionReservationRollsBackWithClaim(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "run", 1)
	if err := s.ConfigureAdmission(state.AdmissionConfig{ProviderRates: map[string]state.AdmissionRate{"shared": {Burst: 1, PerSecond: 0.001}}, Executors: map[string]state.AdmissionDemand{"test": {ProviderKey: "shared"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_admission BEFORE INSERT ON assignments BEGIN SELECT RAISE(ABORT,'injected claim failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("claim should fail")
	}
	for _, table := range []string{"admission_buckets", "admission_reservations", "admission_turns"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial reservation in %s: %d %v", table, n, err)
		}
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_admission`); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "worker"); d == nil {
		t.Fatal("rollback leaked capacity")
	}
}

func TestAdmissionWaitingInputAndTimerUseNoSlots(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	if err := s.ConfigureAdmission(state.AdmissionConfig{GlobalCap: 1}); err != nil {
		t.Fatal(err)
	}
	admissionRun(t, s, "waiting", 0)
	if err := s.SetRunState("waiting", "waiting"); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"input", "timer"} {
		if err := s.CreateActivation(&state.Activation{ID: node, RunID: "waiting", NodeID: node, State: "waiting"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ScheduleTimer("timer", "waiting", "timer", time.Now().Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	admissionRun(t, s, "ready", 1)
	if d, _ := admissionClaim(t, s, "worker"); d == nil || d.RunID != "ready" {
		t.Fatal("input or timer consumed execution capacity")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE run_id='waiting'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("waiting run owns %d slots: %v", count, err)
	}
}

func TestAdmissionQueueReasonTracksPendingActivation(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "run", 2)
	for i := 0; i < 2; i++ {
		if err := s.CreateActivation(&state.Activation{ID: fmt.Sprintf("run-%d", i), RunID: "run", NodeID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	d, g := admissionClaim(t, s, "worker")
	admissionClaim(t, s, "worker")
	acts, err := s.ListActivations("run")
	if err != nil {
		t.Fatal(err)
	}
	if acts[0].QueueReason != "" || acts[1].QueueReason != "Waiting for worker capacity" {
		t.Fatalf("queue reasons: %+v", acts)
	}
	admissionComplete(t, s, "worker", d, g)
	admissionClaim(t, s, "worker")
	acts, err = s.ListActivations("run")
	if err != nil || acts[1].QueueReason != "" {
		t.Fatalf("claimed request retained queue reason: %+v %v", acts, err)
	}
}

func TestAdmissionConfigValidationAndLegacyAssignments(t *testing.T) {
	s := openTemp(t)
	for _, cfg := range []state.AdmissionConfig{
		{GlobalCap: -1}, {PerRunCap: -1}, {KeyCaps: map[string]int{"key": -1}},
		{ProviderRates: map[string]state.AdmissionRate{"provider": {Burst: 0, PerSecond: 1}}},
		{ProviderRates: map[string]state.AdmissionRate{"provider": {Burst: 1, PerSecond: 0}}},
		{Executors: map[string]state.AdmissionDemand{"test": {Slots: -1}}},
	} {
		if err := s.ConfigureAdmission(cfg); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
	admissionWorker(t, s, "one", 10)
	admissionWorker(t, s, "two", 10)
	admissionRun(t, s, "run", 2)
	if d, _ := admissionClaim(t, s, "one"); d == nil {
		t.Fatal("first claim failed")
	}
	// Reproduce a database created before reservations existed.
	if _, err := s.db.Exec(`DELETE FROM admission_reservations`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureAdmission(state.AdmissionConfig{KeyCaps: map[string]int{"shared": 1}, Executors: map[string]state.AdmissionDemand{"test": {OperatorKey: "shared"}}}); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "two"); d != nil {
		t.Fatal("legacy active assignment escaped key budget")
	}
}

func TestAdmissionEqualRunsRotateAndFinishedRunDoesNotBlock(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 1)
	admissionRun(t, s, "a", 3)
	admissionRun(t, s, "b", 2)
	for _, want := range []string{"a", "b", "a", "b", "a"} {
		d, g := admissionClaim(t, s, "worker")
		if d == nil || d.RunID != want {
			t.Fatalf("want run %s, got %+v", want, d)
		}
		admissionComplete(t, s, "worker", d, g)
	}
}

func TestAdmissionSharedResourcesAcrossCapabilities(t *testing.T) {
	for _, scope := range []string{"executor", "key", "provider", "rate"} {
		t.Run(scope, func(t *testing.T) {
			s := openTemp(t)
			admissionWorker(t, s, "one", 10)
			if err := s.RegisterWorker(state.Worker{ID: "two", Capacity: 10, Capabilities: []state.WorkerCapability{{Name: "other"}}, LastSeen: time.Now()}); err != nil {
				t.Fatal(err)
			}
			admissionRun(t, s, "run", 2)
			if _, err := s.db.Exec(`UPDATE dispatches SET executor='other' WHERE activation_id='run-1'`); err != nil {
				t.Fatal(err)
			}
			demand := state.AdmissionDemand{ExecutorKey: "pool", OperatorKey: "tenant", ProviderKey: "external"}
			cfg := state.AdmissionConfig{Executors: map[string]state.AdmissionDemand{"test": demand, "other": demand}}
			switch scope {
			case "executor":
				cfg.ExecutorCaps = map[string]int{"pool": 1}
			case "key":
				cfg.KeyCaps = map[string]int{"tenant": 1}
			case "provider":
				cfg.ProviderCaps = map[string]int{"external": 1}
			case "rate":
				cfg.ProviderRates = map[string]state.AdmissionRate{"external": {Burst: 1, PerSecond: 0.001}}
			}
			if err := s.ConfigureAdmission(cfg); err != nil {
				t.Fatal(err)
			}
			d, g := admissionClaim(t, s, "one")
			if d == nil {
				t.Fatal("first claim blocked")
			}
			if next, _ := admissionClaim(t, s, "two"); next != nil {
				t.Fatalf("%s budget not shared across capabilities", scope)
			}
			admissionComplete(t, s, "one", d, g)
			next, _ := admissionClaim(t, s, "two")
			if scope == "rate" && next != nil {
				t.Fatal("completion refunded rate token")
			}
			if scope != "rate" && next == nil {
				t.Fatal("completion did not free capacity")
			}
		})
	}
}
