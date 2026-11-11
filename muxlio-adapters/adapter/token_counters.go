package adapter

const (
	usageFieldInput uint8 = 1 << iota
	usageFieldOutput
	usageFieldCached
	usageFieldsAll = usageFieldInput | usageFieldOutput | usageFieldCached
)

// TokenCounters contains only reported counts. Nil means unavailable; a non-nil
// value with every field reported is a reported zero. Individual JSON nulls
// preserve unavailable fields so partial provider usage does not look complete.
//
// This adapter-local scratch representation never crosses executor boundaries.
type TokenCounters struct {
	Input      int64
	Output     int64
	Cached     int64
	CacheWrite *int64
	Reasoning  *int64
	Total      *int64
	fields     uint8
	explicit   bool
}

func (u TokenCounters) reportedFields() uint8 {
	if !u.explicit {
		return usageFieldsAll
	}
	return u.fields
}

// Reported returns whether the field was reported by the provider and its count.
func (u TokenCounters) ReportedInput() (int64, bool) {
	return u.Input, u.reportedFields()&usageFieldInput != 0
}
func (u TokenCounters) ReportedOutput() (int64, bool) {
	return u.Output, u.reportedFields()&usageFieldOutput != 0
}
func (u TokenCounters) ReportedCached() (int64, bool) {
	return u.Cached, u.reportedFields()&usageFieldCached != 0
}

func (u TokenCounters) AnyReported() bool {
	return u.reportedFields() != 0
}

func (u TokenCounters) Complete() bool {
	return u.reportedFields() == usageFieldsAll
}

// Equal reports whether two usages carry the same reported availability and values.
func (u TokenCounters) Equal(o TokenCounters) bool {
	equal := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	return u.reportedFields() == o.reportedFields() && u.Input == o.Input && u.Output == o.Output && u.Cached == o.Cached && equal(u.CacheWrite, o.CacheWrite) && equal(u.Reasoning, o.Reasoning) && equal(u.Total, o.Total)
}

// NewTokenCounters records an explicit partial or complete provider usage object.
func NewTokenCounters(input, output, cached *int64) *TokenCounters {
	u := &TokenCounters{explicit: true}
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
		return &TokenCounters{Input: u.Input, Output: u.Output, Cached: u.Cached}
	}
	return u
}

// UnavailableTokenCounters represents an observed usage object with no known dimensions.
func UnavailableTokenCounters() *TokenCounters { return &TokenCounters{explicit: true} }

// OverlayTokenCounters retains confirmed dimensions omitted by a terminal
// report for the same scope. The decoder establishes that scope relationship.
func OverlayTokenCounters(prior, terminal *TokenCounters) *TokenCounters {
	if terminal == nil {
		return prior
	}
	if prior == nil {
		return terminal
	}
	var in, out, ca *int64
	if v, ok := terminal.ReportedInput(); ok {
		in = &v
	} else if v, ok := prior.ReportedInput(); ok {
		in = &v
	}
	if v, ok := terminal.ReportedOutput(); ok {
		out = &v
	} else if v, ok := prior.ReportedOutput(); ok {
		out = &v
	}
	if v, ok := terminal.ReportedCached(); ok {
		ca = &v
	} else if v, ok := prior.ReportedCached(); ok {
		ca = &v
	}
	result := NewTokenCounters(in, out, ca)
	if result == nil {
		return nil
	}
	result.CacheWrite, result.Reasoning = terminal.CacheWrite, terminal.Reasoning
	result.Total = terminal.Total
	if result.Total == nil {
		result.Total = prior.Total
	}
	if result.CacheWrite == nil {
		result.CacheWrite = prior.CacheWrite
	}
	if result.Reasoning == nil {
		result.Reasoning = prior.Reasoning
	}
	return result
}
