package state

import "time"

// Retention configures how long coordinator data survives after the run that
// produced it becomes terminal. Each category is independent so event history,
// stdout/stderr, content-addressed artifacts, and cached plan versions age out
// at different rates. A zero duration means the category is never swept
// automatically; the operator must delete it explicitly. A pinned run is
// always reachable and is never swept, regardless of these durations.
type Retention struct {
	// EventHistory is how long a terminal run's event log is kept.
	EventHistory time.Duration `json:"eventHistory"`
	// StdoutStderr is how long a terminal run's chunked worker output is kept.
	StdoutStderr time.Duration `json:"stdoutStderr"`
	// Artifacts is how long a terminal run's content-addressed outputs remain
	// reachable (and thus protected from GC) after the run ends.
	Artifacts time.Duration `json:"artifacts"`
	// Caches is how long an unreferenced cached plan version is kept before GC.
	Caches time.Duration `json:"caches"`
	// Workspaces retains disposable attempt workspaces after the producing run ends.
	Workspaces time.Duration `json:"workspaces"`
}

// DefaultRetention keeps one day of terminal, unpinned runtime data.
func DefaultRetention() Retention {
	return Retention{
		EventHistory: 24 * time.Hour,
		StdoutStderr: 24 * time.Hour,
		Artifacts:    24 * time.Hour,
		Caches:       24 * time.Hour,
		Workspaces:   24 * time.Hour,
	}
}

// IsTerminalState reports whether a run state is final (no further transitions).
func IsTerminalState(s string) bool {
	switch s {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// ExpiredFor reports whether a run's data in the named category is past
// retention at now and therefore eligible for GC.
//
// A nil run, a pinned run, or a non-terminal run is never expired. An explicit
// RetainedUntil deadline overrides the policy duration for all categories: when
// set, the run expires once now passes it. Otherwise a terminal run expires
// once now passes its UpdatedAt (the moment it became terminal) plus the
// category duration. A zero category duration means never expire, even when
// RetainedUntil is set.
func (r Retention) ExpiredFor(run *Run, category time.Duration, now time.Time) bool {
	if run == nil || run.Pinned {
		return false
	}
	if !IsTerminalState(run.State) {
		return false
	}
	if category == 0 {
		return false
	}
	if run.RetainedUntil != nil {
		return now.After(*run.RetainedUntil)
	}
	return now.After(run.UpdatedAt.Add(category))
}
