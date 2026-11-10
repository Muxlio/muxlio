package plan

import (
	"fmt"
	"math"

	"github.com/muxlio/muxlio-core/internal/state"
)

// RunLimits are pinned with the plan. Nil limits are unlimited; zero forbids
// admission. Work limits are hard pre-dispatch limits, including retries and
// reconciliation queries. MaxActivations includes all structural nodes in every
// nested scope. Active attempts include uncertain operations until resolved.
// Token/cost limits stop further work after measured consumption; they cannot
// cap the bill of an already admitted external operation. Fail-closed (default)
// stops after any attempt lacks observed data, including estimated-only data.
// Fail-open permits unknown usage, but never treats it as zero consumption.
type RunLimits struct {
	MaxAttempts       *int     `json:"maxAttempts,omitempty"`
	MaxActivations    *int     `json:"maxActivations,omitempty"`
	MaxActiveAttempts *int     `json:"maxActiveAttempts,omitempty"`
	MaxTokens         *int64   `json:"maxTokens,omitempty"`
	MaxCostUSD        *float64 `json:"maxCostUsd,omitempty"`
	MissingUsage      string   `json:"missingUsage,omitempty"`
}

func (l *RunLimits) validate() error {
	if l == nil {
		return nil
	}
	for _, n := range []*int{l.MaxAttempts, l.MaxActivations, l.MaxActiveAttempts} {
		if n != nil && *n < 0 {
			return fmt.Errorf("work limits must be non-negative")
		}
	}
	if l.MaxTokens != nil && *l.MaxTokens < 0 {
		return fmt.Errorf("maxTokens must be non-negative")
	}
	if l.MaxCostUSD != nil && (math.IsNaN(*l.MaxCostUSD) || math.IsInf(*l.MaxCostUSD, 0) || *l.MaxCostUSD < 0) {
		return fmt.Errorf("maxCostUsd must be finite and non-negative")
	}
	if l.MissingUsage != "" && l.MissingUsage != "fail-open" && l.MissingUsage != "fail-closed" {
		return fmt.Errorf("missingUsage must be fail-open or fail-closed")
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
	if err := e.checkUsage(attempts, true); err != nil {
		return err
	}
	return e.store.RecordExecutionAttempt(a)
}

func (e *Engine) checkUsage(attempts []state.ExecutionAttempt, admission bool) error {
	l := e.plan.Limits
	if l == nil {
		return nil
	}
	summary := state.SummarizeUsage(attempts)
	for _, metric := range []struct {
		name  string
		limit *float64
		usage state.UsageTotal
	}{
		{"maxTokens", tokenLimit(l.MaxTokens), summary.Tokens},
		{"maxCostUsd", l.MaxCostUSD, summary.CostUSD},
	} {
		if metric.limit == nil {
			continue
		}
		if l.MissingUsage != "fail-open" && (metric.usage.Unknown > 0 || metric.usage.Estimated != nil) {
			return budgetExceeded(metric.name + ": observed usage unavailable (fail-closed)")
		}
		consumed := 0.0
		if metric.usage.Observed != nil {
			consumed += *metric.usage.Observed
		}
		if metric.usage.Estimated != nil {
			consumed += *metric.usage.Estimated
		}
		if consumed > *metric.limit || (admission && consumed >= *metric.limit) {
			return budgetExceeded(metric.name)
		}
	}
	return nil
}

func tokenLimit(n *int64) *float64 {
	if n == nil {
		return nil
	}
	f := float64(*n)
	return &f
}
