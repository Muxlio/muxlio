package accounting

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"strings"
	"testing"
)

func recordedPart(id, model string) Part {
	return Part{Scope: "stream", Entry: contract.AccountingEntry{ID: id, Provider: "fixture", Model: model, Tokens: &contract.TokenMeasures{Total: contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: "1", Source: "fixture", Fidelity: "metered"}}}}}
}
func TestRecorderCompactsDisjointCallsAndRetainsReplayTombstones(t *testing.T) {
	r := NewRecorder()
	for i := 0; i < 1000; i++ {
		if err := r.Record(recordedPart(fmt.Sprintf("call-%d", i), "model")); err != nil {
			t.Fatal(err)
		}
	}
	a, err := r.Snapshot()
	if err != nil || len(a.Entries) != 1 || a.Entries[0].Tokens.Total.Collected.Value != "1000" {
		t.Fatalf("compaction=%+v %v", a, err)
	}
	terminal := recordedPart("terminal", "model")
	terminal.Entry.Tokens.Total.Collected.Value = "1000"
	if err = r.ReplaceScope("stream", []Part{terminal}); err != nil {
		t.Fatal(err)
	}
	revision := r.revision
	if err = r.Record(recordedPart("call-0", "model")); err != nil {
		t.Fatal(err)
	}
	a, err = r.Snapshot()
	if err != nil || r.revision != revision || a.Entries[0].Tokens.Total.Collected.Value != "1000" {
		t.Fatal("superseded delta was counted again")
	}
}
func TestRecorderLimitsNeverCertifyDroppedEvents(t *testing.T) {
	for _, kind := range []string{"events", "buckets", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			limits := RecorderLimits{MaxEvents: 2, MaxBuckets: 2, MaxSnapshotBytes: 2048}
			if kind == "buckets" {
				limits.MaxEvents = 10
				limits.MaxBuckets = 1
			}
			if kind == "bytes" {
				limits.MaxEvents = 10
				limits.MaxBuckets = 10
				limits.MaxSnapshotBytes = 512
			}
			r, err := NewRecorderWithLimits(limits)
			if err != nil {
				t.Fatal(err)
			}
			_ = r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "complete", "")
			failed := false
			for i := 0; i < 10; i++ {
				model := "model"
				if kind != "events" {
					model = fmt.Sprintf("model-%d", i)
				}
				if err = r.Record(recordedPart(fmt.Sprintf("call-%d", i), model)); err != nil {
					if !strings.Contains(err.Error(), "accounting_capacity_exceeded") {
						t.Fatal(err)
					}
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("configured limit ignored")
			}
			_ = r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "complete", "")
			a, err := r.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if a.Coverage[0].State == "complete" || a.Coverage[0].Reason != "accounting_capacity_exceeded" {
				t.Fatalf("dropped observations certified complete: %+v", a.Coverage)
			}
		})
	}
}
func TestRecorderDistinctIdentitiesAndConflict(t *testing.T) {
	r := NewRecorder()
	_ = r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "complete", "")
	_ = r.Record(recordedPart("a", "model"))
	_ = r.Record(recordedPart("b", "model"))
	bad := recordedPart("a", "model")
	bad.Entry.Tokens.Total.Collected.Value = "2"
	if err := r.Record(bad); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	a, err := r.Finish()
	if err != nil || a.Entries[0].Tokens.Total.Collected.Value != "2" || a.Coverage[0].State != "partial" {
		t.Fatalf("confirmed values changed: %+v %v", a, err)
	}
}
