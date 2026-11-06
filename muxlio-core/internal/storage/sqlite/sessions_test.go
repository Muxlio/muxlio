package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestSessionReservationsAndWorkerAffinity(t *testing.T) {
	s := openTemp(t)
	manifest := &contract.AdapterManifest{Executor: "test", ExecutorVersion: "1", Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}
	for _, id := range []string{"owner", "other"} {
		if err := s.RegisterWorker(state.Worker{ID: id, Capacity: 4, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{{Name: "test", Version: "1", Contract: manifest}}, Labels: map[string]string{workers.SessionEnvironmentLabel: "env"}}); err != nil {
			t.Fatal(err)
		}
	}
	handle := &contract.SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "owner", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour), Recoverable: true}
	raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", Effect: "pure", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
	for i := range 2 {
		id := fmt.Sprint(i)
		if err := s.CreateRun(&state.Run{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: id, RunID: id, ActivationID: id, AttemptID: id, Executor: "test", Request: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := admissionClaim(t, s, "other"); d != nil {
		t.Fatal("session migrated to another worker")
	}
	first, generation := admissionClaim(t, s, "owner")
	if first == nil {
		t.Fatal("owner could not claim")
	}
	if second, _ := admissionClaim(t, s, "owner"); second != nil {
		t.Fatal("concurrent session prompts admitted")
	}
	admissionComplete(t, s, "owner", first, generation)
	if second, _ := admissionClaim(t, s, "owner"); second == nil {
		t.Fatal("completion did not release session reservation")
	}
}

func TestLostSessionReservationSurvivesCoordinatorRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &contract.AdapterManifest{Executor: "test", Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}
	if err := s.RegisterWorker(state.Worker{ID: "owner", Capacity: 2, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{{Name: "test", Contract: manifest}}, Labels: map[string]string{workers.SessionEnvironmentLabel: "env"}}); err != nil {
		t.Fatal(err)
	}
	handle := &contract.SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "owner", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour), Recoverable: true}
	// Even a prompt labelled pure mutates persistent conversation state.
	raw, err := json.Marshal(workers.ExecuteRequest{Executor: "test", Effect: "pure", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
	if err != nil {
		t.Fatal(err)
	}
	admissionRun(t, s, "run", 2)
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json=?`, string(raw)); err != nil {
		t.Fatal(err)
	}
	first, _, err := s.ClaimDispatchForEpoch("owner", time.Now().Add(time.Hour), epoch)
	if err != nil || first == nil {
		t.Fatalf("claim: %+v %v", first, err)
	}
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
	if next, _, err := s.ClaimDispatchForEpoch("owner", time.Now().Add(time.Hour), epoch); err != nil || next != nil {
		t.Fatalf("lost session replayed or overlapped: %+v %v", next, err)
	}
	assignment, err := s.LookupWorkerAssignment("owner", first.AssignmentID)
	if err != nil || assignment == nil || assignment.State != "uncertain" {
		t.Fatalf("lost session was not fenced: %+v %v", assignment, err)
	}
}
