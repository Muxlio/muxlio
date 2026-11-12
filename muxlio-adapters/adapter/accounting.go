package adapter

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
)

// CollectedTokenMeasures maps normalized counters into canonical readings.
// Source interpretation and inclusive-cache normalization belong to the caller.
func CollectedTokenMeasures(usage *TokenCounters, source string) *contract.TokenMeasures {
	if usage == nil {
		return nil
	}
	measure := func(n int64, ok bool) contract.Measure[contract.Count] {
		if !ok {
			return contract.Measure[contract.Count]{}
		}
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(fmt.Sprint(n)), Source: source, Fidelity: "metered"}}
	}
	in, hasIn := usage.ReportedInput()
	out, hasOut := usage.ReportedOutput()
	cache, hasCache := usage.ReportedCached()
	tokens := &contract.TokenMeasures{Input: measure(in, hasIn), Output: measure(out, hasOut), CacheRead: measure(cache, hasCache)}
	if usage.CacheWrite != nil {
		tokens.CacheWrite = measure(*usage.CacheWrite, true)
	}
	if usage.Reasoning != nil {
		tokens.Reasoning = measure(*usage.Reasoning, true)
	}
	if usage.Total != nil {
		tokens.Total = measure(*usage.Total, true)
	} else if hasIn && hasOut {
		if total, err := accounting.AddCount(contract.Count(fmt.Sprint(in)), contract.Count(fmt.Sprint(out))); err == nil {
			tokens.Total.Collected = &contract.Reading[contract.Count]{Value: total, Source: source, Fidelity: "derived-exact"}
		}
	}
	return tokens
}

func FailWithAccounting(snapshot *contract.Accounting, format string, args ...any) {
	_ = WriteResponse(Response{Outputs: map[string]any{}, Error: fmt.Sprintf(format, args...), Accounting: snapshot})
	os.Exit(0)
}

// TokenRecorder accepts already normalized inclusive counters. It replaces the
// same invocation scope and publishes only changed full snapshots.
type TokenRecorder struct {
	recorder     *accounting.Recorder
	source       string
	lastRevision uint64
	publisher    *AccountingPublisher
}

func NewTokenRecorder(source string) *TokenRecorder {
	r := accounting.NewRecorder()
	_ = r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "unknown", "no-confirmed-report")
	return &TokenRecorder{recorder: r, source: source, publisher: NewAccountingPublisher()}
}
func (r *TokenRecorder) Update(usage *TokenCounters) error {
	return r.observe(usage, false)
}

// Replace applies a decoder-certified terminal replacement, including an
// explicit correction to earlier counters in the same invocation scope.
func (r *TokenRecorder) Replace(usage *TokenCounters) error {
	return r.observe(usage, true)
}

func (r *TokenRecorder) ReportModel(id, role, source string) error {
	return r.recorder.ReportModel(contract.AccountingModel{ID: id, Role: role, Source: source})
}

// Delta supplies a decoder-established stable event identity. The shared
// recorder owns replay suppression and checked aggregation.
func (r *TokenRecorder) Delta(id string, usage *TokenCounters) error {
	if usage == nil {
		return nil
	}
	if err := r.recorder.ObserveDelta(accounting.Part{Scope: "invocation", Entry: contract.AccountingEntry{ID: id, Tokens: CollectedTokenMeasures(usage, r.source)}}); err != nil {
		return err
	}
	return r.publish(false)
}

func (r *TokenRecorder) publish(material bool) error {
	_ = r.recorder.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "partial", "source-coverage-unverified")
	a, err := r.recorder.Snapshot()
	if err != nil {
		return err
	}
	if a.Revision == r.lastRevision {
		return nil
	}
	if err := r.publisher.Publish(*a, material); err != nil {
		return err
	}
	r.lastRevision = a.Revision
	return nil
}

func (r *TokenRecorder) observe(usage *TokenCounters, replacement bool) error {
	if usage == nil {
		return nil
	}
	part := accounting.Part{Scope: "invocation", Entry: contract.AccountingEntry{ID: "invocation", Tokens: CollectedTokenMeasures(usage, r.source)}}
	var observationErr error
	if replacement {
		observationErr = r.recorder.ReplaceTokenScope(part.Scope, part.Entry)
	} else {
		observationErr = r.recorder.ObserveCumulative(part)
	}
	if err := observationErr; err != nil {
		return err
	}
	return r.publish(replacement)
}
func (r *TokenRecorder) Finish() *contract.Accounting {
	a, err := FinalizeAccounting(r.recorder, r.publisher)
	if err != nil {
		AppendLogChunk("stderr", "accounting-finalization-failed: confirmed progress remains partial")
		return nil
	}
	return a
}

// These operations accept only canonical evidence. Protocol interpretation and
// ownership validation remain in each adapter.
func (r *TokenRecorder) ReportSession(s contract.AccountingSession) error {
	return r.recorder.ReportSession(s)
}
func (r *TokenRecorder) SetCoverage(k contract.AccountingKey, state, reason string) error {
	return r.recorder.SetCoverage(k, state, reason)
}
func (r *TokenRecorder) ReplaceAccountingScope(scope string, parts []accounting.Part) error {
	if err := r.recorder.ReplaceScope(scope, parts); err != nil {
		return err
	}
	return r.publish(true)
}
