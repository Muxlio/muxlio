package accounting

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

const MaxIdentifiedEvents = 100000
const MaxCompactBuckets = 256
const MaxSnapshotBytes = 64 << 10

type RecorderLimits struct{ MaxEvents, MaxBuckets, MaxSnapshotBytes int }

// Part is a decoder-confirmed disjoint observation with logical scope identity.
type Part struct {
	Scope string
	Entry contract.AccountingEntry
}
type eventIdentity struct{ scope, digest string }
type Recorder struct {
	revision uint64
	final    bool
	entries  map[string]Part
	seen     map[string]eventIdentity
	buckets  map[string]contract.AccountingEntry
	coverage map[contract.AccountingKey]contract.MeterCoverage
	limits   RecorderLimits
	anomaly  string
	models   []contract.AccountingModel
}

func NewRecorder() *Recorder {
	r, _ := NewRecorderWithLimits(RecorderLimits{MaxIdentifiedEvents, MaxCompactBuckets, MaxSnapshotBytes})
	return r
}
func NewRecorderWithLimits(l RecorderLimits) (*Recorder, error) {
	if l.MaxEvents < 1 || l.MaxEvents > MaxIdentifiedEvents || l.MaxBuckets < 1 || l.MaxBuckets > MaxCompactBuckets || l.MaxSnapshotBytes < 512 || l.MaxSnapshotBytes > MaxSnapshotBytes {
		return nil, fmt.Errorf("invalid accounting recorder limits")
	}
	return &Recorder{revision: 1, entries: map[string]Part{}, seen: map[string]eventIdentity{}, buckets: map[string]contract.AccountingEntry{}, coverage: map[contract.AccountingKey]contract.MeterCoverage{}, limits: l}, nil
}
func copyEntry(e contract.AccountingEntry) (contract.AccountingEntry, []byte, error) {
	raw, err := contract.CanonicalAccounting(contract.Accounting{Revision: 1, Entries: []contract.AccountingEntry{e}})
	if err != nil {
		return e, nil, err
	}
	var a contract.Accounting
	if err = json.Unmarshal(raw, &a); err != nil {
		return e, nil, err
	}
	return a.Entries[0], raw, nil
}
func (r *Recorder) reject(reason string) error {
	if r.anomaly == "" {
		r.anomaly = reason
		r.revision++
		for k, c := range r.coverage {
			if c.State != "inapplicable" {
				if c.State == "complete" {
					c.State = "partial"
				}
				c.Reason = reason
				r.coverage[k] = c
			}
		}
		if len(r.coverage) == 0 {
			k := contract.AccountingKey{Kind: "tokens"}
			r.coverage[k] = contract.MeterCoverage{Key: k, State: "unknown", Reason: reason}
		}
	}
	return fmt.Errorf("%s", reason)
}
func (r *Recorder) check(buckets map[string]contract.AccountingEntry) error {
	if len(buckets) > r.limits.MaxBuckets {
		return fmt.Errorf("accounting_capacity_exceeded")
	}
	a := r.snapshot(buckets)
	// Reserve room for revision growth and an explicit capacity diagnostic.
	a.Revision = 9007199254740991
	raw, err := contract.CanonicalAccounting(a)
	if err != nil {
		return err
	}
	if len(raw)+128 > r.limits.MaxSnapshotBytes {
		return fmt.Errorf("accounting_capacity_exceeded")
	}
	return nil
}
func (r *Recorder) Record(part Part) error {
	if r.final {
		return fmt.Errorf("accounting recorder is final")
	}
	if part.Scope == "" || len(part.Scope) > 120 {
		return r.reject("accounting_scope_invalid")
	}
	entry, raw, err := copyEntry(part.Entry)
	if err != nil {
		return r.reject("accounting_observation_invalid")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if prior, ok := r.seen[entry.ID]; ok {
		if prior.scope == part.Scope && prior.digest == digest {
			return nil
		}
		return r.reject("accounting_identity_conflict")
	}
	if len(r.seen) >= r.limits.MaxEvents {
		return r.reject("accounting_capacity_exceeded")
	}
	key, bucket, err := compactEntry(entry)
	if err != nil {
		return r.reject("accounting_observation_invalid")
	}
	if prior, ok := r.buckets[key]; ok {
		bucket, err = addEntries(prior, bucket)
		if err != nil {
			return r.reject("accounting_overflow")
		}
	}
	next := make(map[string]contract.AccountingEntry, len(r.buckets)+1)
	for k, v := range r.buckets {
		next[k] = v
	}
	next[key] = bucket
	if err = r.check(next); err != nil {
		return r.reject("accounting_capacity_exceeded")
	}
	part.Entry = entry
	r.entries[entry.ID] = part
	r.seen[entry.ID] = eventIdentity{part.Scope, digest}
	r.buckets = next
	r.revision++
	return nil
}

// ReplaceScope is for decoder-certified full replacements. Event tombstones
// remain, so replayed deltas superseded by a terminal report cannot reappear.
func (r *Recorder) ReplaceScope(scope string, parts []Part) error {
	if r.final {
		return fmt.Errorf("accounting recorder is final")
	}
	if scope == "" || len(scope) > 120 {
		return r.reject("accounting_scope_invalid")
	}
	next := make(map[string]Part, len(r.entries)+len(parts))
	identities := make(map[string]eventIdentity, len(parts))
	for id, p := range r.entries {
		if p.Scope != scope {
			next[id] = p
		}
	}
	for _, p := range parts {
		if p.Scope != scope {
			return r.reject("accounting_scope_invalid")
		}
		if _, ok := next[p.Entry.ID]; ok {
			return r.reject("accounting_identity_conflict")
		}
		e, raw, err := copyEntry(p.Entry)
		if err != nil {
			return r.reject("accounting_observation_invalid")
		}
		p.Entry = e
		if prior, ok := r.seen[e.ID]; ok && prior.scope != scope {
			return r.reject("accounting_identity_conflict")
		}
		identities[e.ID] = eventIdentity{scope, fmt.Sprintf("%x", sha256.Sum256(raw))}
		next[e.ID] = p
	}
	count := len(r.seen)
	for id := range identities {
		if _, seen := r.seen[id]; !seen {
			count++
		}
	}
	if count > r.limits.MaxEvents {
		return r.reject("accounting_capacity_exceeded")
	}
	buckets := map[string]contract.AccountingEntry{}
	for _, p := range next {
		key, bucket, err := compactEntry(p.Entry)
		if err != nil {
			return r.reject("accounting_observation_invalid")
		}
		if prior, ok := buckets[key]; ok {
			bucket, err = addEntries(prior, bucket)
			if err != nil {
				return r.reject("accounting_overflow")
			}
		}
		buckets[key] = bucket
	}
	if err := r.check(buckets); err != nil {
		return r.reject("accounting_capacity_exceeded")
	}
	before, _ := contract.CanonicalAccounting(r.snapshot(r.buckets))
	after, _ := contract.CanonicalAccounting(r.snapshot(buckets))
	r.entries = next
	for id, identity := range identities {
		r.seen[id] = identity
	}
	r.buckets = buckets
	if !bytes.Equal(before, after) {
		r.revision++
	}
	return nil
}
func (r *Recorder) SetCoverage(key contract.AccountingKey, state, reason string) error {
	if r.final {
		return fmt.Errorf("accounting recorder is final")
	}
	if r.anomaly != "" && state != "inapplicable" {
		if state == "complete" {
			state = "partial"
		}
		reason = r.anomaly
	}
	c := contract.MeterCoverage{Key: key, State: state, Reason: reason}
	if err := (&contract.Accounting{Revision: 1, Coverage: []contract.MeterCoverage{c}}).Validate(); err != nil {
		return err
	}
	if prior, ok := r.coverage[key]; ok && prior == c {
		return nil
	}
	if len(r.coverage) >= 64 {
		if _, ok := r.coverage[key]; !ok {
			return r.reject("accounting_capacity_exceeded")
		}
	}
	prior, exists := r.coverage[key]
	r.coverage[key] = c
	if err := r.check(r.buckets); err != nil {
		if exists {
			r.coverage[key] = prior
		} else {
			delete(r.coverage, key)
		}
		return r.reject("accounting_capacity_exceeded")
	}
	r.revision++
	return nil
}
func (r *Recorder) snapshot(buckets map[string]contract.AccountingEntry) contract.Accounting {
	revision := r.revision
	if revision == 0 {
		revision = 1
	}
	a := contract.Accounting{Revision: revision, Final: r.final, Models: r.models}
	for _, e := range buckets {
		a.Entries = append(a.Entries, e)
	}
	for _, c := range r.coverage {
		a.Coverage = append(a.Coverage, c)
	}
	return a
}

func (r *Recorder) ReportModel(m contract.AccountingModel) error {
	if r.final {
		return fmt.Errorf("accounting recorder is final")
	}
	for _, prior := range r.models {
		if prior == m {
			return nil
		}
	}
	if len(r.models) >= 32 {
		return r.reject("accounting_capacity_exceeded")
	}
	if err := (&contract.Accounting{Revision: 1, Models: []contract.AccountingModel{m}}).Validate(); err != nil {
		return err
	}
	r.models = append(r.models, m)
	if err := r.check(r.buckets); err != nil {
		r.models = r.models[:len(r.models)-1]
		return r.reject("accounting_capacity_exceeded")
	}
	r.revision++
	return nil
}
func (r *Recorder) Snapshot() (*contract.Accounting, error) {
	a := r.snapshot(r.buckets)
	raw, err := contract.CanonicalAccounting(a)
	if err != nil {
		return nil, err
	}
	if len(raw) > r.limits.MaxSnapshotBytes {
		return nil, fmt.Errorf("accounting_capacity_exceeded")
	}
	var copyA contract.Accounting
	if err = json.Unmarshal(raw, &copyA); err != nil {
		return nil, err
	}
	return &copyA, nil
}
func (r *Recorder) Finish() (*contract.Accounting, error) {
	if !r.final {
		r.final = true
		r.revision++
	}
	return r.Snapshot()
}
