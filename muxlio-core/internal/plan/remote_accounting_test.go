package plan

import (
	"fmt"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestRemotePollProgressAndTaskAccountingKeepSeparateOwners(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	polls := 0
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		ref := &contract.RemoteTaskReference{ID: "remote-1", Pending: true, PollAt: time.Now().Add(-time.Second)}
		if req.Action == "" {
			a := testaccounting.Tokens(100, 0, 0)
			a.Final = false
			return workers.ExecuteResult{RemoteTask: ref, Accounting: a}, nil
		}
		polls++
		// Lookup work publishes progress even when its terminal result omits it.
		if err := req.AccountingSink.Append(*testaccounting.Tokens(2, 0, 0)); err != nil {
			return workers.ExecuteResult{}, err
		}
		remote := testaccounting.Tokens(100, 0, 0)
		remote.Final = false
		if polls == 10 {
			ref.Pending = false
			remote = testaccounting.Tokens(150, 0, 0)
			remote.Revision = 2
		}
		return workers.ExecuteResult{RemoteTask: ref, RemoteAccounting: &contract.RemoteAccountingUpdate{TaskID: ref.ID, SourceRevision: fmt.Sprint(remote.Revision), Accounting: *remote}}, nil
	}}
	e := NewEngineWithStore(&Plan{}, f, s)
	if _, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := state.SummarizeAccounting(attempts)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 10 || len(attempts) != 11 || summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != "170" {
		t.Fatalf("lookup and operation accounting mixed: polls=%d attempts=%d summary=%+v", polls, len(attempts), summary.Tokens)
	}
}
