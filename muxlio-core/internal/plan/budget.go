package plan

import (
	"fmt"
	"strings"

	"github.com/muxlio/muxlio-adapters/accounting"

	"github.com/muxlio/muxlio-core/internal/state"
)

// RunLimits are pinned with the plan. Nil limits are unlimited; zero forbids
// admission. Work limits are hard pre-dispatch limits, including retries and
// reconciliation queries. MaxActivations includes all structural nodes in every
// nested scope. Active attempts include uncertain operations until resolved.
// Accounting limits stop further work after recorded consumption; they cannot
// cap the bill of an already admitted external operation. Fail-closed (default)
// stops after any finished attempt lacks eligible accounting evidence; usage of
// in-flight attempts is pending, but their recorded readings still count.
// Fail-open permits unknown usage, but never treats it as zero consumption.
type RunLimits struct {
	Accounting        []accounting.Limit `json:"accounting,omitempty"`
	MaxAttempts       *int               `json:"maxAttempts,omitempty"`
	MaxActivations    *int               `json:"maxActivations,omitempty"`
	MaxActiveAttempts *int               `json:"maxActiveAttempts,omitempty"`
}

func (l *RunLimits) validate() error {
	if l == nil {
		return nil
	}
	keys := map[any]bool{}
	for _, limit := range l.Accounting {
		if err := limit.Validate(); err != nil {
			return err
		}
		key := limit.Key()
		if keys[key] {
			return fmt.Errorf("duplicate accounting limit selector")
		}
		keys[key] = true
	}
	for _, n := range []*int{l.MaxAttempts, l.MaxActivations, l.MaxActiveAttempts} {
		if n != nil && *n < 0 {
			return fmt.Errorf("work limits must be non-negative")
		}
	}

	return nil
}

func budgetExceeded(reason string) error {
	return fmt.Errorf("muxlio: run budget exhausted: %s", reason)
}

// budgetMu covers each admission's durable count and insertion so concurrent
// map branches cannot consume the same remaining slot. Counts come from the
// store, surviving engine reconstruction and explicit run retries.
func (e *Engine) admitActivation(a *state.Activation) error {
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	if l := e.plan.Limits; l != nil && l.MaxActivations != nil {
		acts, err := e.store.ListActivations(a.RunID)
		if err != nil {
			return err
		}
		for _, old := range acts {
			if old.ID == a.ID {
				return nil
			}
		}
		if len(acts) >= *l.MaxActivations {
			return budgetExceeded("maxActivations")
		}
	}
	return e.store.CreateActivation(a)
}

func (e *Engine) admitAttempt(a state.ExecutionAttempt) error {
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	if err := e.checkpoint(); err != nil {
		return err
	}
	l := e.plan.Limits
	attempts, err := e.store.ListExecutionAttempts(a.RunID, "")
	if err != nil {
		return err
	}
	// Reattaching an admitted durable operation does not consume another slot.
	for _, old := range attempts {
		if old.ID == a.ID {
			return nil
		}
	}
	if l == nil {
		return e.store.RecordExecutionAttempt(a)
	}
	if l.MaxAttempts != nil && len(attempts) >= *l.MaxAttempts {
		return budgetExceeded("maxAttempts")
	}
	active := 0
	for _, old := range attempts {
		if old.State == "queued" || old.State == "running" || old.State == "awaiting_reconciliation" || old.State == "cancelled" || old.AwaitReconciliationReason != "" {
			active++
		}
	}
	if l.MaxActiveAttempts != nil && active >= *l.MaxActiveAttempts {
		return budgetExceeded("maxActiveAttempts")
	}
	if err := e.checkAccounting(attempts, true); err != nil {
		return err
	}
	return e.store.RecordExecutionAttempt(a)
}

func (e *Engine) checkAccounting(attempts []state.ExecutionAttempt, admission bool) error {
	l := e.plan.Limits
	if l == nil {
		return nil
	}
	views := make([]accounting.AttemptView, 0, len(attempts))
	finished := make([]accounting.AttemptView, 0, len(attempts))
	for _, a := range attempts {
		view := accounting.AttemptView{State: a.State, Accounting: a.Accounting}
		views = append(views, view)
		if !accountingInFlight(a.State) {
			finished = append(finished, view)
		}
	}
	for _, limit := range l.Accounting {
		err := accounting.CheckLimit(views, limit, admission)
		if err == nil {
			continue
		}
		if !strings.HasPrefix(err.Error(), "missing-accounting:") || len(finished) == len(views) {
			return err
		}
		// Recorded consumption of in-flight siblings still counts toward the
		// maximum above. Their final usage is pending rather than missing, so
		// only finished attempts decide the missing-accounting policy.
		if err := accounting.CheckLimit(finished, limit, admission); err != nil {
			return err
		}
	}
	return nil
}

// accountingInFlight reports attempts whose final usage cannot be reported yet.
func accountingInFlight(state string) bool {
	switch state {
	case "queued", "running", "detached", "awaiting_reconciliation", "uncertain", "waiting", "suspended":
		return true
	}
	return false
}
