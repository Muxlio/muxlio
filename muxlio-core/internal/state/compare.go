package state

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/accounting"
	"sort"
)

// ActivationComparison aligns independent histories by structural identity.
type ActivationComparison struct {
	ScopePath []string    `json:"scopePath"`
	NodeID    string      `json:"nodeId"`
	Before    *Activation `json:"before"`
	After     *Activation `json:"after"`
	Changed   bool        `json:"changed"`
}

type RunComparison struct {
	BeforeAccounting accounting.AccountingSummary `json:"beforeAccounting"`
	AfterAccounting  accounting.AccountingSummary `json:"afterAccounting"`
	Before           *Run                         `json:"before"`
	After            *Run                         `json:"after"`
	BeforePlan       json.RawMessage              `json:"beforePlan"`
	AfterPlan        json.RawMessage              `json:"afterPlan"`
	InputsChanged    bool                         `json:"inputsChanged"`
	PlanChanged      bool                         `json:"planChanged"`
	ResultsChanged   bool                         `json:"resultsChanged"`
	Activations      []ActivationComparison       `json:"activations"`
	ReusedOutputs    []Event                      `json:"reusedOutputs"`
}

func sameJSON(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && string(x) == string(y)
}

// CompareRuns reads both histories without modifying either run. Costs remain
// separate observed/estimated/unavailable totals rather than invented deltas.
func CompareRuns(s Store, beforeID, afterID string) (*RunComparison, error) {
	before, err := s.GetRun(beforeID)
	if err != nil {
		return nil, err
	}
	after, err := s.GetRun(afterID)
	if err != nil {
		return nil, err
	}
	ba, err := s.ListActivations(beforeID)
	if err != nil {
		return nil, err
	}
	aa, err := s.ListActivations(afterID)
	if err != nil {
		return nil, err
	}
	bt, err := s.ListExecutionAttempts(beforeID, "")
	if err != nil {
		return nil, err
	}
	at, err := s.ListExecutionAttempts(afterID, "")
	if err != nil {
		return nil, err
	}
	events, err := s.GetEvents(afterID, 0, 0)
	if err != nil {
		return nil, err
	}
	beforeAccounting, err := SummarizeAccounting(bt)
	if err != nil {
		return nil, err
	}
	afterAccounting, err := SummarizeAccounting(at)
	if err != nil {
		return nil, err
	}
	c := &RunComparison{Before: before, After: after, BeforePlan: before.PlanJSON, AfterPlan: after.PlanJSON, InputsChanged: !sameJSON(before.Input, after.Input), PlanChanged: before.PlanDigest != after.PlanDigest, ResultsChanged: !sameJSON(before.Output, after.Output) || before.State != after.State || before.Error != after.Error, Activations: []ActivationComparison{}, ReusedOutputs: []Event{}}
	c.BeforeAccounting, c.AfterAccounting = beforeAccounting, afterAccounting
	aligned := map[string]*ActivationComparison{}
	for _, a := range ba {
		key, _ := json.Marshal([]any{append([]string{}, a.ScopePath...), a.NodeID, a.Occurrence})
		aligned[string(key)] = &ActivationComparison{ScopePath: a.ScopePath, NodeID: a.NodeID, Before: &a}
	}
	for _, a := range aa {
		key, _ := json.Marshal([]any{append([]string{}, a.ScopePath...), a.NodeID, a.Occurrence})
		row := aligned[string(key)]
		if row == nil {
			row = &ActivationComparison{ScopePath: a.ScopePath, NodeID: a.NodeID}
			aligned[string(key)] = row
		}
		row.After = &a
	}
	keys := make([]string, 0, len(aligned))
	for key := range aligned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := aligned[key]
		row.Changed = row.Before == nil || row.After == nil
		if !row.Changed {
			row.Changed = !sameJSON(row.Before.Inputs, row.After.Inputs) || !sameJSON(row.Before.Outputs, row.After.Outputs) || row.Before.State != row.After.State || row.Before.Error != row.After.Error
		}
		if row.Before == nil || row.After == nil || !sameJSON(row.Before.Outputs, row.After.Outputs) || row.Before.State != row.After.State || row.Before.Error != row.After.Error {
			c.ResultsChanged = true
		}
		c.Activations = append(c.Activations, *row)
	}
	for _, event := range events {
		if event.Type == "activation.output_reused" {
			c.ReusedOutputs = append(c.ReusedOutputs, event)
		}
	}
	return c, nil
}
