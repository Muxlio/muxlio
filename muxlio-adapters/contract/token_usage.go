package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

const (
	usageFieldInput uint8 = 1 << iota
	usageFieldOutput
	usageFieldCached
	usageFieldsAll = usageFieldInput | usageFieldOutput | usageFieldCached
)

// TokenUsage contains only reported counts. Nil means unavailable; a non-nil
// value with every field reported is a reported zero. Individual JSON nulls
// preserve unavailable fields so partial provider usage does not look complete.
//
// Go zero values (fields unset) remain backward-compatible with legacy records
// that reported every dimension.
type TokenUsage struct {
	Input    int64
	Output   int64
	Cached   int64
	fields   uint8
	explicit bool
}

func (u TokenUsage) reportedFields() uint8 {
	if !u.explicit {
		return usageFieldsAll
	}
	return u.fields
}

// Reported returns whether the field was reported by the provider and its count.
func (u TokenUsage) ReportedInput() (int64, bool) {
	return u.Input, u.reportedFields()&usageFieldInput != 0
}
func (u TokenUsage) ReportedOutput() (int64, bool) {
	return u.Output, u.reportedFields()&usageFieldOutput != 0
}
func (u TokenUsage) ReportedCached() (int64, bool) {
	return u.Cached, u.reportedFields()&usageFieldCached != 0
}

func (u TokenUsage) AnyReported() bool {
	return u.reportedFields() != 0
}

func (u TokenUsage) Complete() bool {
	return u.reportedFields() == usageFieldsAll
}

// Equal reports whether two usages carry the same reported availability and values.
func (u TokenUsage) Equal(o TokenUsage) bool {
	return u.reportedFields() == o.reportedFields() && u.Input == o.Input && u.Output == o.Output && u.Cached == o.Cached
}

func (u TokenUsage) MarshalJSON() ([]byte, error) {
	fields := u.reportedFields()
	m := make(map[string]any, 3)
	if fields&usageFieldInput != 0 {
		m["input"] = u.Input
	} else {
		m["input"] = nil
	}
	if fields&usageFieldOutput != 0 {
		m["output"] = u.Output
	} else {
		m["output"] = nil
	}
	if fields&usageFieldCached != 0 {
		m["cached"] = u.Cached
	} else {
		m["cached"] = nil
	}
	return json.Marshal(m)
}

func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	next := TokenUsage{explicit: true}
	if err := decodeUsageCount(raw["input"], &next.Input, usageFieldInput, &next.fields); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	if err := decodeUsageCount(raw["output"], &next.Output, usageFieldOutput, &next.fields); err != nil {
		return fmt.Errorf("output: %w", err)
	}
	if err := decodeUsageCount(raw["cached"], &next.Cached, usageFieldCached, &next.fields); err != nil {
		return fmt.Errorf("cached: %w", err)
	}
	normalizeLegacyCompleteUsage(&next)
	*u = next
	return nil
}

func normalizeLegacyCompleteUsage(u *TokenUsage) {
	if u == nil || !u.explicit || u.fields != usageFieldsAll {
		return
	}
	u.explicit = false
	u.fields = 0
}

func decodeUsageCount(raw json.RawMessage, dst *int64, bit uint8, fields *uint8) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var n *int64
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&n); err != nil {
		return fmt.Errorf("invalid count")
	}
	if n == nil {
		return nil
	}
	if *n < 0 {
		return fmt.Errorf("count must be non-negative")
	}
	*dst = *n
	*fields |= bit
	return nil
}

// NewTokenUsage records an explicit partial or complete provider usage object.
func NewTokenUsage(input, output, cached *int64) *TokenUsage {
	u := &TokenUsage{explicit: true}
	if input != nil {
		u.Input = *input
		u.fields |= usageFieldInput
	}
	if output != nil {
		u.Output = *output
		u.fields |= usageFieldOutput
	}
	if cached != nil {
		u.Cached = *cached
		u.fields |= usageFieldCached
	}
	if u.fields == 0 {
		return nil
	}
	if u.fields == usageFieldsAll {
		legacy := &TokenUsage{Input: u.Input, Output: u.Output, Cached: u.Cached}
		return legacy
	}
	return u
}

// UnavailableTokenUsage represents an observed usage object with no known dimensions.
func UnavailableTokenUsage() *TokenUsage { return &TokenUsage{explicit: true} }

func addUsageCount(total, add int64) (int64, error) {
	if add < 0 {
		return 0, fmt.Errorf("count must be non-negative")
	}
	if total > math.MaxInt64-add {
		return 0, fmt.Errorf("token count overflow")
	}
	return total + add, nil
}

// AddTokenUsage sums dimensions reported by both records and retains a
// dimension reported by only one record.
func AddTokenUsage(total, reported *TokenUsage) (*TokenUsage, error) {
	if reported == nil {
		return total, nil
	}
	if total == nil {
		if err := validateTokenUsage(reported); err != nil {
			return nil, err
		}
		copy := *reported
		return &copy, nil
	}
	if err := validateTokenUsage(total); err != nil {
		return nil, err
	}
	if err := validateTokenUsage(reported); err != nil {
		return nil, err
	}
	sum := TokenUsage{}
	for _, merge := range []struct {
		bit uint8
		get func(TokenUsage) (int64, bool)
		set func(*TokenUsage, int64)
	}{
		{usageFieldInput, TokenUsage.ReportedInput, func(u *TokenUsage, v int64) { u.Input = v }},
		{usageFieldOutput, TokenUsage.ReportedOutput, func(u *TokenUsage, v int64) { u.Output = v }},
		{usageFieldCached, TokenUsage.ReportedCached, func(u *TokenUsage, v int64) { u.Cached = v }},
	} {
		leftOK := total.reportedFields()&merge.bit != 0
		rightOK := reported.reportedFields()&merge.bit != 0
		if !leftOK && !rightOK {
			continue
		}
		var v int64
		if leftOK && rightOK {
			l, _ := merge.get(*total)
			r, _ := merge.get(*reported)
			var err error
			v, err = addUsageCount(l, r)
			if err != nil {
				return nil, err
			}
		} else if leftOK {
			v, _ = merge.get(*total)
		} else {
			v, _ = merge.get(*reported)
		}
		sum.fields |= merge.bit
		merge.set(&sum, v)
	}
	sum.explicit = true
	normalizeLegacyCompleteUsage(&sum)
	return &sum, nil
}

func validateTokenUsage(u *TokenUsage) error {
	if u == nil {
		return nil
	}
	fields := u.reportedFields()
	if fields&usageFieldInput != 0 && u.Input < 0 {
		return fmt.Errorf("executor usage must be non-negative")
	}
	if fields&usageFieldOutput != 0 && u.Output < 0 {
		return fmt.Errorf("executor usage must be non-negative")
	}
	if fields&usageFieldCached != 0 && u.Cached < 0 {
		return fmt.Errorf("executor usage must be non-negative")
	}
	return nil
}
