package state

import (
	"sort"
	"strings"
	"time"
)

// AnalyticsQuery bounds run and attempt reads for the analytics explorer.
// Run outcomes use createdAt; attempt usage bucketing follows client-side
// finishedAt → startedAt → run createdAt semantics on the returned attempts.
type AnalyticsQuery struct {
	CreatedAfter  time.Time
	CreatedBefore time.Time
	State         string
	Workflow      string
	Executor      string
	Offset        int
	Limit         int
}

// AnalyticsSnapshot is a page of run headers plus summary attempts (no results/logs).
type AnalyticsSnapshot struct {
	Runs          []*Run
	AttemptsByRun map[string][]ExecutionAttempt
	TotalRuns     int
	StateCounts   map[string]int
	Offset        int
	Limit         int
	HasMore       bool
	Populations   AnalyticsPopulations
}

type AnalyticsPopulations struct {
	RunOutcomes   string `json:"runOutcomes"`
	AttemptUsage  string `json:"attemptUsage"`
	BucketingNote string `json:"bucketingNote"`
}

// AnalyticsStore serves bounded analytics reads.
type AnalyticsStore interface {
	QueryAnalytics(AnalyticsQuery) (*AnalyticsSnapshot, error)
}

func splitCSVFilter(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runMatchesAnalyticsFilters(r *Run, q AnalyticsQuery, states []string) bool {
	if !q.CreatedAfter.IsZero() && r.CreatedAt.Before(q.CreatedAfter) {
		return false
	}
	if !q.CreatedBefore.IsZero() && r.CreatedAt.After(q.CreatedBefore) {
		return false
	}
	if len(states) > 0 {
		match := false
		for _, s := range states {
			if r.State == s {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	} else if q.State != "" && r.State != q.State {
		return false
	}
	if q.Workflow != "" && !strings.Contains(strings.ToLower(r.WorkflowName), strings.ToLower(q.Workflow)) {
		return false
	}
	return true
}

func filterAttemptsByExecutor(attempts []ExecutionAttempt, executor string) []ExecutionAttempt {
	if executor == "" {
		return attempts
	}
	needle := strings.ToLower(executor)
	out := make([]ExecutionAttempt, 0, len(attempts))
	for _, a := range attempts {
		if strings.Contains(strings.ToLower(a.Executor), needle) {
			out = append(out, a)
		}
	}
	return out
}

func stripAttemptDetails(a ExecutionAttempt) ExecutionAttempt {
	a.Result = nil
	a.OperationRef = nil
	a.Failure = ""
	return a
}

func (s *Memory) QueryAnalytics(q AnalyticsQuery) (*AnalyticsSnapshot, error) {
	states := splitCSVFilter(q.State)
	runs, err := s.ListRuns()
	if err != nil {
		return nil, err
	}
	filtered := []*Run{}
	counts := map[string]int{}
	for _, r := range runs {
		counts[r.State]++
		if !runMatchesAnalyticsFilters(r, q, states) {
			continue
		}
		if q.Executor != "" {
			attempts, err := s.ListExecutionAttempts(r.ID, "")
			if err != nil {
				return nil, err
			}
			if len(filterAttemptsByExecutor(attempts, q.Executor)) == 0 {
				continue
			}
		}
		copy := *r
		copy.Input = nil
		copy.Output = nil
		copy.PlanJSON = nil
		copy.Error = ""
		filtered = append(filtered, &copy)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	total := len(filtered)
	offset := q.Offset
	if offset > total {
		offset = total
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	attemptsByRun := map[string][]ExecutionAttempt{}
	for _, r := range page {
		attempts, err := s.ListExecutionAttemptPage(r.ID, "", 0, -1, false)
		if err != nil {
			return nil, err
		}
		for i := range attempts {
			attempts[i] = stripAttemptDetails(attempts[i])
		}
		attempts = filterAttemptsByExecutor(attempts, q.Executor)
		attemptsByRun[r.ID] = attempts
	}
	filteredCounts := map[string]int{}
	for _, r := range filtered {
		filteredCounts[r.State]++
	}
	return &AnalyticsSnapshot{
		Runs:          page,
		AttemptsByRun: attemptsByRun,
		TotalRuns:     total,
		StateCounts:   filteredCounts,
		Offset:        offset,
		Limit:         limit,
		HasMore:       end < total,
		Populations:   DefaultAnalyticsPopulations(),
	}, nil
}

func DefaultAnalyticsPopulations() AnalyticsPopulations {
	return AnalyticsPopulations{
		RunOutcomes:   "Runs counted by createdAt within the selected time window.",
		AttemptUsage:  "Usage and timing aggregate attempts; trend buckets use attempt finishedAt, then startedAt, then run createdAt.",
		BucketingNote: "Daily/hourly chart buckets use UTC calendar boundaries; labels use the selected viewer timezone.",
	}
}
