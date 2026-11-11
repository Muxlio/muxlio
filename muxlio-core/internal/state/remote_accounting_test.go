package state_test

import (
	"context"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"path/filepath"
	"testing"
)

func TestRemoteAccountingOwnerRevisionAndReplay(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			old := otel.GetMeterProvider()
			otel.SetMeterProvider(provider)
			defer otel.SetMeterProvider(old)
			defer provider.Shutdown(context.Background())
			var s state.Store
			path := filepath.Join(t.TempDir(), "remote.db")
			if backend == "memory" {
				s = state.New()
			} else {
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				s = db
			}
			defer func() { _ = s.Close() }()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.CreateRun(&state.Run{ID: "run"}))
			ref := &contract.RemoteTaskReference{ID: "task", URL: "https://remote.example/rpc", ContextID: "context", Pending: true}
			initial := testaccounting.Tokens(10, 0, 0)
			initial.Final = false
			origin := state.ExecutionAttempt{ID: "origin", RunID: "run", ActivationID: "work", Executor: "generic", State: "detached", OperationRef: ref, Accounting: initial}
			must(s.RecordExecutionAttempt(origin))
			terminal := *ref
			terminal.Pending = false
			query := state.ExecutionAttempt{ID: "poll", RunID: "run", ActivationID: "poll", Executor: "generic", State: "succeeded", OperationAttemptID: "origin", OperationAction: "poll", OperationRef: &terminal, Accounting: testaccounting.Tokens(2, 0, 0)}
			must(s.RecordExecutionAttempt(query))
			snapshot := testaccounting.Tokens(100, 0, 0)
			snapshot.Revision = 2
			update := contract.RemoteAccountingUpdate{TaskID: "task", SourceRevision: "source-2", Accounting: *snapshot}
			for i := 0; i < 10; i++ {
				must(s.RecordRemoteAccountingUpdate("poll", update))
			}
			if backend == "sqlite" {
				must(s.Close())
				reopened, err := sqlite.Open(path)
				must(err)
				s = reopened
				must(s.RecordRemoteAccountingUpdate("poll", update))
			}
			events, err := s.GetEvents("run", 0, 100)
			must(err)
			updates := 0
			for _, event := range events {
				if event.Type == state.EventAccountingUpdated {
					updates++
				}
			}
			if updates != 1 {
				t.Fatalf("replayed remote receipt emitted %d updates", updates)
			}
			var metrics metricdata.ResourceMetrics
			if err := reader.Collect(context.Background(), &metrics); err != nil {
				t.Fatal(err)
			}
			foundMetric := false
			for _, scope := range metrics.ScopeMetrics {
				for _, m := range scope.Metrics {
					if m.Name == "gen_ai.client.inference.usage.input_tokens" {
						foundMetric = true
						data := m.Data.(metricdata.Sum[int64])
						if len(data.DataPoints) != 1 || data.DataPoints[0].Value != 100 {
							t.Fatalf("remote ownership/replay duplicated usage: %+v", data)
						}
					}
				}
			}
			if !foundMetric {
				t.Fatal("final remote accounting did not emit usage")
			}
			rows, err := s.ListExecutionAttempts("run", "")
			must(err)
			summary, err := state.SummarizeAccounting(rows)
			must(err)
			if summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != "102" {
				t.Fatalf("poll replay duplicated consumption: %+v", summary)
			}
			conflict := update
			conflict.Accounting = *testaccounting.Tokens(101, 0, 0)
			conflict.Accounting.Revision = 2
			if err = s.RecordRemoteAccountingUpdate("poll", conflict); err == nil {
				t.Fatal("conflicting source revision accepted")
			}
			changed := update
			changed.TaskID = "other"
			if err = s.RecordRemoteAccountingUpdate("poll", changed); err == nil {
				t.Fatal("changed task accepted")
			}
			changed = update
			changed.SourceRevision = "source-3"
			changed.Accounting.Revision = 3
			if err = s.RecordRemoteAccountingUpdate("poll", changed); err == nil {
				t.Fatal("final owner changed")
			}
			query.OperationAttemptID = "other-owner"
			if err = s.RecordExecutionAttempt(query); err == nil {
				t.Fatal("query owner mutated")
			}
		})
	}
}

func TestRemoteAccountingCannotFinalizePendingTask(t *testing.T) {
	s := state.New()
	defer s.Close()
	_ = s.CreateRun(&state.Run{ID: "run"})
	ref := &contract.RemoteTaskReference{ID: "task", Pending: true}
	_ = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "owner", RunID: "run", Executor: "generic", OperationRef: ref})
	_ = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "poll", RunID: "run", Executor: "generic", State: "succeeded", OperationAction: "poll", OperationAttemptID: "owner", OperationRef: ref})
	a := testaccounting.Tokens(1, 0, 0)
	if err := s.RecordRemoteAccountingUpdate("poll", contract.RemoteAccountingUpdate{TaskID: "task", SourceRevision: "1", Accounting: *a}); err == nil {
		t.Fatal("pending remote task finalized")
	}
}
