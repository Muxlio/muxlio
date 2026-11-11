package state

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

type AccountingSummary = accounting.AccountingSummary

// SummarizeAccounting maps persisted attempts into the shared provider-neutral
// reducer. Arithmetic remains in muxlio-adapters/accounting.
func SummarizeAccounting(attempts []ExecutionAttempt) (accounting.AccountingSummary, error) {
	views := make([]accounting.AttemptView, 0, len(attempts))
	for _, a := range attempts {
		views = append(views, accounting.AttemptView{State: a.State, Accounting: a.Accounting})
	}
	return accounting.SummarizeAccounting(views)
}

// AccountingForAttempt validates an accounting snapshot at state boundaries.
func AccountingForAttempt(a ExecutionAttempt) (*contract.Accounting, error) {
	if a.Accounting == nil {
		return nil, nil
	}
	raw, err := contract.CanonicalAccounting(*a.Accounting)
	if err != nil {
		return nil, err
	}
	var snapshot contract.Accounting
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
