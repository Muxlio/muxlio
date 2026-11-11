package accounting

import (
	"sort"

	"github.com/muxlio/muxlio-adapters/contract"
)

type AttemptView struct {
	State         string
	Accounting    *contract.Accounting
	Applicability []contract.MeterCoverage
}

type SummaryAmount struct {
	Evidence                  *SummaryEvidence     `json:"evidence,omitempty"`
	CollectedSubtotal         *string              `json:"collectedSubtotal"`
	EstimatedFallbackSubtotal *string              `json:"estimatedFallbackSubtotal"`
	KnownSubtotal             *string              `json:"knownSubtotal"`
	EffectiveTotal            *string              `json:"effectiveTotal"`
	Completeness              string               `json:"completeness"`
	Attempts                  SummaryAttemptCounts `json:"attempts"`
}
type SummaryEvidence struct {
	Collected []string `json:"collected,omitempty"`
	Estimated []string `json:"estimated,omitempty"`
}
type SummaryAttemptCounts struct {
	Complete     int `json:"complete"`
	Partial      int `json:"partial"`
	Unknown      int `json:"unknown"`
	Pending      int `json:"pending"`
	Inapplicable int `json:"inapplicable"`
}
type TokenDimensionSummary struct {
	Input      SummaryAmount `json:"input"`
	Output     SummaryAmount `json:"output"`
	CacheRead  SummaryAmount `json:"cacheRead"`
	CacheWrite SummaryAmount `json:"cacheWrite"`
	Reasoning  SummaryAmount `json:"reasoning"`
}
type CreditSummary struct {
	Provider string        `json:"provider"`
	Unit     string        `json:"unit"`
	Value    SummaryAmount `json:"value"`
}
type CostSummary struct {
	Currency string        `json:"currency"`
	Basis    string        `json:"basis"`
	Value    SummaryAmount `json:"value"`
}
type UnattributedSummary struct {
	Credits UnknownPending `json:"credits"`
	Costs   UnknownPending `json:"costs"`
}
type UnknownPending struct {
	UnknownAttempts int `json:"unknownAttempts"`
	PendingAttempts int `json:"pendingAttempts"`
}
type AccountingSummary struct {
	Tokens          SummaryAmount         `json:"tokens"`
	TokenDimensions TokenDimensionSummary `json:"tokenDimensions"`
	Credits         []CreditSummary       `json:"credits"`
	Costs           []CostSummary         `json:"costs"`
	Unattributed    UnattributedSummary   `json:"unattributed"`
}

type meterAcc struct {
	evidence                    map[string]map[string]bool
	collected, estimated, known *string
	count                       SummaryAttemptCounts
}

func noteEvidence[T ~string](a *meterAcc, m contract.Measure[T]) {
	collected, estimated := selected(m)
	origin, fidelity := "", ""
	if collected != "" {
		origin, fidelity = "collected", m.Collected.Fidelity
	} else if estimated != "" {
		origin, fidelity = "estimated", m.Estimated.Fidelity
	}
	if origin == "" {
		return
	}
	if a.evidence == nil {
		a.evidence = map[string]map[string]bool{}
	}
	if a.evidence[origin] == nil {
		a.evidence[origin] = map[string]bool{}
	}
	a.evidence[origin][fidelity] = true
}
func evidenceFor(a *meterAcc) *SummaryEvidence {
	if len(a.evidence) == 0 {
		return nil
	}
	e := &SummaryEvidence{}
	for fidelity := range a.evidence["collected"] {
		e.Collected = append(e.Collected, fidelity)
	}
	for fidelity := range a.evidence["estimated"] {
		e.Estimated = append(e.Estimated, fidelity)
	}
	sort.Strings(e.Collected)
	sort.Strings(e.Estimated)
	return e
}

func addString(dst **string, v string, count bool) error {
	if count {
		n, err := AddCount(contract.Count(valueOrZero(*dst)), contract.Count(v))
		if err != nil {
			return err
		}
		s := string(n)
		*dst = &s
		return nil
	}
	n, err := AddAmount(contract.Amount(valueOrZero(*dst)), contract.Amount(v))
	if err != nil {
		return err
	}
	s := string(n)
	*dst = &s
	return nil
}
func valueOrZero(v *string) string {
	if v == nil {
		return "0"
	}
	return *v
}
func addReading(a *meterAcc, collected, estimated string, count bool) error {
	if collected != "" {
		if err := addString(&a.collected, collected, count); err != nil {
			return err
		}
		if err := addString(&a.known, collected, count); err != nil {
			return err
		}
	} else if estimated != "" {
		if err := addString(&a.estimated, estimated, count); err != nil {
			return err
		}
		if err := addString(&a.known, estimated, count); err != nil {
			return err
		}
	}
	return nil
}
func coverageFor(view AttemptView, key contract.AccountingKey) string {
	a := view.Accounting
	if a == nil {
		for _, c := range view.Applicability {
			if c.Key == key {
				return c.State
			}
		}
		for _, c := range view.Applicability {
			if c.Key == (contract.AccountingKey{Kind: key.Kind}) {
				return c.State
			}
		}
		return "unknown"
	}
	for _, c := range a.Coverage {
		if c.Key == key {
			return c.State
		}
	}
	for _, c := range a.Coverage {
		if c.Key == (contract.AccountingKey{Kind: key.Kind}) {
			return c.State
		}
	}
	for _, c := range view.Applicability {
		if c.Key == key {
			return c.State
		}
	}
	for _, c := range view.Applicability {
		if c.Key == (contract.AccountingKey{Kind: key.Kind}) {
			return c.State
		}
	}
	return "unknown"
}
func unattributedState(view AttemptView, kind string) string {
	identified := false
	if view.Accounting != nil {
		for _, c := range view.Accounting.Coverage {
			k := c.Key
			if k.Kind == kind {
				identified = true
			}
			if k.Kind == kind && k.Provider == "" && k.Unit == "" && k.Currency == "" && k.Basis == "" {
				return c.State
			}
		}
	}
	for _, c := range view.Applicability {
		k := c.Key
		if k.Kind == kind {
			identified = true
		}
		if k.Kind == kind && k.Provider == "" && k.Unit == "" && k.Currency == "" && k.Basis == "" {
			return c.State
		}
	}
	if identified {
		return "inapplicable"
	}
	return "unknown"
}
func selected[T ~string](m contract.Measure[T]) (string, string) {
	if m.Collected != nil && m.Collected.Fidelity != "proxy" {
		return string(m.Collected.Value), ""
	}
	if m.Estimated != nil && m.Estimated.Fidelity != "proxy" {
		return "", string(m.Estimated.Value)
	}
	return "", ""
}

func tokenTotalConflict(t *contract.TokenMeasures) bool {
	if t == nil {
		return false
	}
	var total, input, output *contract.Reading[contract.Count]
	if t.Total.Collected != nil && t.Input.Collected != nil && t.Output.Collected != nil {
		total, input, output = t.Total.Collected, t.Input.Collected, t.Output.Collected
	} else if t.Total.Estimated != nil && t.Input.Estimated != nil && t.Output.Estimated != nil {
		total, input, output = t.Total.Estimated, t.Input.Estimated, t.Output.Estimated
	} else {
		return false
	}
	if total.Fidelity == "proxy" || input.Fidelity == "proxy" || output.Fidelity == "proxy" || total.Source != input.Source || total.Source != output.Source || total.Version != input.Version || total.Version != output.Version || total.Method != input.Method || total.Method != output.Method {
		return false
	}
	sum, err := AddCount(input.Value, output.Value)
	return err != nil || sum != total.Value
}
func finish(a *meterAcc, views []AttemptView, key contract.AccountingKey, dimension ...string) SummaryAmount {
	for _, v := range views {
		state := coverageFor(v, key)
		if state == "inapplicable" && meterReported(v, key) {
			state = "partial"
		}
		// Coverage is a source assertion, not a substitute for eligible
		// readings. One empty attempt must not make another attempt's subtotal
		// appear to cover both executions.
		if state == "complete" && len(dimension) == 0 {
			known, missing := false, false
			if v.Accounting != nil {
				for _, entry := range v.Accounting.Entries {
					usable, relevant := false, false
					switch key.Kind {
					case "tokens":
						if entry.Tokens == nil {
							continue
						}
						relevant = true
						c, e := selected(entry.Tokens.Total)
						usable = c != "" || e != ""
						if !usable {
							ic, ie := selected(entry.Tokens.Input)
							oc, oe := selected(entry.Tokens.Output)
							usable = (ic != "" || ie != "") && (oc != "" || oe != "")
						}
						if tokenTotalConflict(entry.Tokens) {
							usable = false
						}
					case "credits":
						for _, meter := range entry.Credits {
							if meter.Provider == key.Provider && meter.Unit == key.Unit {
								relevant = true
								c, e := selected(meter.Amount)
								usable = c != "" || e != ""
							}
						}
					case "cost":
						for _, meter := range entry.Costs {
							if meter.Currency == key.Currency && meter.Basis == key.Basis {
								relevant = true
								c, e := selected(meter.Amount)
								usable = c != "" || e != ""
							}
						}
					}
					if relevant {
						if usable {
							known = true
						} else {
							missing = true
						}
					}
				}
			}
			if !known {
				state = "unknown"
			} else if missing {
				state = "partial"
			}
		}
		if key.Kind == "tokens" && len(dimension) == 0 && v.Accounting != nil {
			for _, entry := range v.Accounting.Entries {
				if tokenTotalConflict(entry.Tokens) {
					state = "partial"
					break
				}
			}
		}
		if len(dimension) > 0 && state != "inapplicable" && v.Accounting != nil {
			known, missing := false, false
			for _, entry := range v.Accounting.Entries {
				if entry.Tokens == nil {
					continue
				}
				t := entry.Tokens
				var m contract.Measure[contract.Count]
				switch dimension[0] {
				case "input":
					m = t.Input
				case "output":
					m = t.Output
				case "cacheRead":
					m = t.CacheRead
				case "cacheWrite":
					m = t.CacheWrite
				case "reasoning":
					m = t.Reasoning
				}
				c, e := selected(m)
				if c != "" || e != "" {
					known = true
				} else {
					missing = true
				}
			}
			if !known {
				state = "unknown"
			} else if missing {
				state = "partial"
			}
		}
		if pendingAccounting(v.State) && state != "inapplicable" {
			a.count.Pending++
			continue
		}
		if state == "complete" && (v.Accounting == nil || !v.Accounting.Final) {
			state = "partial"
		}
		switch state {
		case "complete":
			a.count.Complete++
		case "partial":
			a.count.Partial++
		case "inapplicable":
			a.count.Inapplicable++
		default:
			a.count.Unknown++
		}
	}
	comp := summaryCompleteness(a.count)
	var effective *string
	if a.known != nil && comp == "complete" {
		v := *a.known
		effective = &v
	}
	return SummaryAmount{Evidence: evidenceFor(a), CollectedSubtotal: a.collected, EstimatedFallbackSubtotal: a.estimated, KnownSubtotal: a.known, EffectiveTotal: effective, Completeness: comp, Attempts: a.count}
}

func summaryCompleteness(c SummaryAttemptCounts) string {
	if c.Unknown > 0 {
		return "unknown"
	}
	if c.Partial > 0 || c.Pending > 0 {
		return "partial"
	}
	if c.Complete > 0 {
		return "complete"
	}
	return "inapplicable"
}

// A declared inapplicable meter cannot erase readings, including partial or
// zero readings. Applicability supplied separately from a snapshot is subject
// to the same rule as snapshot coverage.
func meterReported(v AttemptView, key contract.AccountingKey) bool {
	if v.Accounting == nil {
		return false
	}
	for _, e := range v.Accounting.Entries {
		switch key.Kind {
		case "tokens":
			if t := e.Tokens; t != nil {
				for _, m := range []contract.Measure[contract.Count]{t.Total, t.Input, t.Output, t.CacheRead, t.CacheWrite, t.Reasoning} {
					if m.Collected != nil || m.Estimated != nil {
						return true
					}
				}
			}
		case "credits":
			for _, m := range e.Credits {
				if m.Provider == key.Provider && m.Unit == key.Unit && (m.Amount.Collected != nil || m.Amount.Estimated != nil) {
					return true
				}
			}
		case "cost":
			for _, m := range e.Costs {
				if m.Currency == key.Currency && m.Basis == key.Basis && (m.Amount.Collected != nil || m.Amount.Estimated != nil) {
					return true
				}
			}
		}
	}
	return false
}

func pendingAccounting(state string) bool {
	switch state {
	case "running", "queued", "uncertain", "awaiting_reconciliation", "waiting", "suspended":
		return true
	}
	return false
}

// SummarizeAccounting folds persisted latest attempt snapshots. Entries are
// disjoint scopes; collected and estimated readings on one measure never add.
func SummarizeAccounting(views []AttemptView) (AccountingSummary, error) {
	out := AccountingSummary{Credits: []CreditSummary{}, Costs: []CostSummary{}}
	token := &meterAcc{}
	dims := map[string]*meterAcc{}
	for _, k := range []string{"input", "output", "cacheRead", "cacheWrite", "reasoning"} {
		dims[k] = &meterAcc{}
	}
	credits := map[string]*meterAcc{}
	costs := map[string]*meterAcc{}
	creditID := map[string][2]string{}
	costID := map[string][2]string{}
	for _, v := range views {
		if v.Accounting == nil {
			continue
		}
		for _, e := range v.Accounting.Entries {
			if e.Tokens != nil {
				if tokenTotalConflict(e.Tokens) {
					// Conflicting full reports leave consumption unavailable; dimension
					// subtotals below remain useful without choosing either total.
				} else if c, x := selected(e.Tokens.Total); c != "" || x != "" {
					c, x := selected(e.Tokens.Total)
					noteEvidence(token, e.Tokens.Total)
					if err := addReading(token, c, x, true); err != nil {
						return out, err
					}
				} else {
					ic, ie := selected(e.Tokens.Input)
					oc, oe := selected(e.Tokens.Output)
					fullScope := coverageFor(v, contract.AccountingKey{Kind: "tokens"}) == "complete"
					if fullScope && ic != "" && oc != "" {
						noteEvidence(token, e.Tokens.Input)
						noteEvidence(token, e.Tokens.Output)
						n, err := AddCount(contract.Count(ic), contract.Count(oc))
						if err != nil {
							return out, err
						}
						if err = addReading(token, string(n), "", true); err != nil {
							return out, err
						}
					} else if fullScope && (ic != "" || ie != "") && (oc != "" || oe != "") {
						if token.evidence == nil {
							token.evidence = map[string]map[string]bool{}
						}
						if token.evidence["estimated"] == nil {
							token.evidence["estimated"] = map[string]bool{}
						}
						token.evidence["estimated"]["derived-exact"] = true
						n, err := AddCount(contract.Count(valueOrZeroFrom(ic, ie)), contract.Count(valueOrZeroFrom(oc, oe)))
						if err != nil {
							return out, err
						}
						if err = addReading(token, "", string(n), true); err != nil {
							return out, err
						}
					}
				}
				for name, m := range map[string]contract.Measure[contract.Count]{"input": e.Tokens.Input, "output": e.Tokens.Output, "cacheRead": e.Tokens.CacheRead, "cacheWrite": e.Tokens.CacheWrite, "reasoning": e.Tokens.Reasoning} {
					c, x := selected(m)
					noteEvidence(dims[name], m)
					if err := addReading(dims[name], c, x, true); err != nil {
						return out, err
					}
				}
			}
			for _, c := range e.Credits {
				k := c.Provider + "\x00" + c.Unit
				if credits[k] == nil {
					credits[k] = &meterAcc{}
					creditID[k] = [2]string{c.Provider, c.Unit}
				}
				x, y := selected(c.Amount)
				noteEvidence(credits[k], c.Amount)
				if err := addReading(credits[k], x, y, false); err != nil {
					return out, err
				}
			}
			for _, c := range e.Costs {
				k := c.Currency + "\x00" + c.Basis
				if costs[k] == nil {
					costs[k] = &meterAcc{}
					costID[k] = [2]string{c.Currency, c.Basis}
				}
				x, y := selected(c.Amount)
				noteEvidence(costs[k], c.Amount)
				if err := addReading(costs[k], x, y, false); err != nil {
					return out, err
				}
			}
		}
	}
	out.Tokens = finish(token, views, contract.AccountingKey{Kind: "tokens"})
	out.TokenDimensions = TokenDimensionSummary{Input: finish(dims["input"], views, contract.AccountingKey{Kind: "tokens"}, "input"), Output: finish(dims["output"], views, contract.AccountingKey{Kind: "tokens"}, "output"), CacheRead: finish(dims["cacheRead"], views, contract.AccountingKey{Kind: "tokens"}, "cacheRead"), CacheWrite: finish(dims["cacheWrite"], views, contract.AccountingKey{Kind: "tokens"}, "cacheWrite"), Reasoning: finish(dims["reasoning"], views, contract.AccountingKey{Kind: "tokens"}, "reasoning")}
	for k, a := range credits {
		id := creditID[k]
		out.Credits = append(out.Credits, CreditSummary{Provider: id[0], Unit: id[1], Value: finish(a, views, contract.AccountingKey{Kind: "credits", Provider: id[0], Unit: id[1]})})
	}
	sort.Slice(out.Credits, func(i, j int) bool {
		if out.Credits[i].Provider == out.Credits[j].Provider {
			return out.Credits[i].Unit < out.Credits[j].Unit
		}
		return out.Credits[i].Provider < out.Credits[j].Provider
	})
	for k, a := range costs {
		id := costID[k]
		out.Costs = append(out.Costs, CostSummary{Currency: id[0], Basis: id[1], Value: finish(a, views, contract.AccountingKey{Kind: "cost", Currency: id[0], Basis: id[1]})})
	}
	sort.Slice(out.Costs, func(i, j int) bool {
		if out.Costs[i].Currency == out.Costs[j].Currency {
			return out.Costs[i].Basis < out.Costs[j].Basis
		}
		return out.Costs[i].Currency < out.Costs[j].Currency
	})
	for _, v := range views {
		creditState := unattributedState(v, "credits")
		costState := unattributedState(v, "cost")
		pending := pendingAccounting(v.State)
		if creditState != "inapplicable" {
			if pending {
				out.Unattributed.Credits.PendingAttempts++
			} else if creditState == "unknown" || creditState == "partial" {
				out.Unattributed.Credits.UnknownAttempts++
			}
		}
		if costState != "inapplicable" {
			if pending {
				out.Unattributed.Costs.PendingAttempts++
			} else if costState == "unknown" || costState == "partial" {
				out.Unattributed.Costs.UnknownAttempts++
			}
		}
	}
	return out, nil
}
func valueOrZeroFrom(a, b string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return "0"
}
