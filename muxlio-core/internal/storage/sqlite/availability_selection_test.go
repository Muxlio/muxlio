package sqlite

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestSelectAnyDispatchSkipsUnavailableExecutor(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "w1", 2, "agent/a", "agent/b")
	now := time.Now()
	if err := s.MarkExecutorUnavailable(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/a"}, contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota",
	}, now); err != nil {
		t.Fatal(err)
	}
	selectionDispatch(t, s, "any", workers.ExecuteRequest{})
	d, _ := admissionClaim(t, s, "w1")
	if d == nil {
		t.Fatal("expected claim against available agent/b")
	}
	if d.Executor != "agent/b" {
		t.Fatalf("selected %q, want agent/b (skip unavailable agent/a)", d.Executor)
	}
}

func TestAvailabilityTrialUsesCoordinatorLeaseAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		terminal     string
		availability bool
		started      bool
		newerBlock   bool
	}{
		{name: "succeeded", terminal: "succeeded", started: true},
		{name: "failed", terminal: "failed", started: true},
		{name: "renewed quota", terminal: "failed", availability: true, started: true},
		{name: "no launch evidence", terminal: "succeeded"},
		{name: "newer block", terminal: "succeeded", started: true, newerBlock: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			selectionRun(t, s)
			selectionWorker(t, s, "remote", 3, "agent/test")
			binding := state.ExecutorBinding{WorkerID: "remote", Executor: "agent/test"}
			now := time.Now()
			future := now.Add(time.Hour)
			report := contract.Availability{Reason: contract.AvailabilityQuotaExhausted, RetryAt: &future}
			if err := s.MarkExecutorUnavailable(binding, report, now); err != nil {
				t.Fatal(err)
			}
			selectionDispatch(t, s, "trial", workers.ExecuteRequest{Executor: "agent/test"})
			selectionDispatch(t, s, "next", workers.ExecuteRequest{Executor: "agent/test"})
			if d, _ := admissionClaim(t, s, "remote"); d != nil {
				t.Fatalf("claimed before cooldown: %+v", d)
			}
			past := now.Add(-time.Minute)
			report.RetryAt = &past
			if err := s.MarkExecutorUnavailable(binding, report, now); err != nil {
				t.Fatal(err)
			}
			trial, generation := admissionClaim(t, s, "remote")
			if trial == nil || trial.AttemptID != "trial" {
				t.Fatalf("expected a trial assignment: %+v", trial)
			}
			if d, _ := admissionClaim(t, s, "remote"); d != nil {
				t.Fatalf("claimed a second assignment during recovery: %+v", d)
			}
			if tc.newerBlock {
				report.RetryAt = &future
				if err := s.MarkExecutorUnavailable(binding, report, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			result := workers.ExecuteResult{}
			if tc.started {
				started := time.Now()
				result.WorkerStartedAt = &started
			}
			if tc.availability {
				report.RetryAt = nil
				result.Availability = &report
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			a := state.Assignment{RunID: trial.RunID, ActivationID: trial.ActivationID, AssignmentID: trial.AssignmentID,
				AttemptID: trial.AttemptID, WorkerID: "remote", Generation: generation}
			if accepted, err := s.CompleteWorkerAssignment(a, tc.terminal, time.Now(), raw); err != nil || !accepted {
				t.Fatalf("completion accepted=%v err=%v", accepted, err)
			}
			block, err := s.GetExecutorUnavailability(binding)
			if err != nil {
				t.Fatal(err)
			}
			next, _ := admissionClaim(t, s, "remote")
			if tc.terminal == "succeeded" && tc.started && !tc.newerBlock {
				if block != nil || next == nil || next.AttemptID != "next" {
					t.Fatalf("successful assignment should unblock the executor: %+v %+v", block, next)
				}
			} else if block == nil || block.RetryAt == nil || !block.RetryAt.After(now) || next != nil {
				t.Fatalf("expected cooldown and no claim: %+v %+v", block, next)
			}
		})
	}
}
