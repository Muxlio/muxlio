// Package admission provides fair, resource-aware admission control for
// workflow step dispatch. It implements atomic multi-scope capacity
// reservation, per-provider token-bucket rate limiting, per-run concurrency
// caps, and deficit round-robin scheduling between run keys so that a large
// fan-out run cannot indefinitely starve a newly-arrived unrelated run.
//
// Architecture contract: the Gate is the sole authority on whether a pending
// dispatch may be admitted. It must be consulted atomically (under the same
// lock that claims the dispatch from the queue) so no capacity can be
// double-counted. The Gate is coordinator-resident and provider-agnostic; it
// knows only executor capability names, run IDs, and configurable limits.
// Provider-specific protocol details stay in adapters.
//
// Scope hierarchy (all limits are hard – dispatch is blocked if any is
// violated):
//
//  1. Worker capacity   – total active slots on the claiming worker
//  2. Global hard cap   – total active slots across all workers (optional)
//  3. Per-run cap       – max concurrent dispatches from one run (optional)
//  4. Per-key cap       – max concurrent dispatches sharing an operator key
//                         (executor capability prefix, optional)
//  5. Token-bucket rate – burst+rate per provider key (optional)
//
// Fairness: deficit round-robin (DRR) between run IDs. Each run is assigned
// a virtual deficit counter that grows by its weight on each scheduling round
// and decreases by one for each admitted dispatch. A run is eligible to be
// scheduled only when its deficit is positive and no hard limit blocks it.
// Runs not seen for one full rotation are reset to avoid stale negative
// deficits accumulating.
package admission

import (
	"sync"
	"time"
)

// DispatchInfo carries the per-dispatch metadata the Gate needs to make an
// admission decision. It must not contain provider-specific fields.
type DispatchInfo struct {
	// RunID is the workflow run that owns this dispatch.
	RunID string
	// Executor is the capability name (e.g. "openai/gpt-4o").
	// The provider key is derived from the executor name by the Gate according
	// to ProviderKeyFunc (defaults to the full executor string).
	Executor string
	// OperatorKey is an optional grouping key set by the operator (e.g. an
	// API-key name, cost-centre, or tenant ID). Empty means "no keyed limit".
	OperatorKey string
}

// QueueReason is a human-readable token explaining why a dispatch is waiting.
// The coordinator can surface this in the UI without branching on internals.
type QueueReason string

const (
	QueueReasonWorkerFull    QueueReason = "worker_full"
	QueueReasonGlobalFull    QueueReason = "global_full"
	QueueReasonRunCap        QueueReason = "run_cap"
	QueueReasonKeyCap        QueueReason = "key_cap"
	QueueReasonRateLimit     QueueReason = "rate_limit"
	QueueReasonFairness      QueueReason = "fairness"
	QueueReasonNotApplicable QueueReason = ""
)

// TokenBucketConfig configures a per-provider token bucket.
type TokenBucketConfig struct {
	// Burst is the maximum number of tokens the bucket can hold (also the
	// initial fill). Must be >= 1.
	Burst int
	// RatePerSecond is how many tokens are added per second. Must be > 0.
	RatePerSecond float64
}

// Config holds all tunable limits for the admission Gate.
// Zero values mean "no limit" for capacity-style fields and "disabled" for
// feature-style fields.
type Config struct {
	// GlobalCap is the maximum number of simultaneously active dispatches
	// across all workers. Zero means unlimited.
	GlobalCap int

	// PerRunCap is the maximum number of simultaneously active dispatches from
	// a single workflow run. Zero means unlimited.
	PerRunCap int

	// PerKeyCaps maps operator key → max concurrent active dispatches. Keys
	// not listed here are unlimited (unless DefaultKeyCap > 0).
	PerKeyCaps map[string]int

	// DefaultKeyCap applies to every non-empty OperatorKey not listed in
	// PerKeyCaps. Zero means unlimited.
	DefaultKeyCap int

	// ProviderRateLimits maps provider key → token bucket config. The
	// provider key is derived from the executor name by ProviderKeyFunc.
	ProviderRateLimits map[string]TokenBucketConfig

	// ProviderKeyFunc extracts a provider key from an executor capability name.
	// If nil, the executor name itself is used as the provider key.
	ProviderKeyFunc func(executor string) string

	// RunWeight returns the DRR weight for a run ID (default 1 if nil or
	// returns 0). Higher weight means more scheduling turns per round.
	RunWeight func(runID string) int

	// PrefetchLimit is the maximum number of dispatches the Gate will mark
	// as eligible for a single ClaimDispatch scan. Zero uses the default (8).
	// This bounds how far ahead of demand the Gate looks.
	PrefetchLimit int
}

// bucket is a simple token bucket for rate limiting.
type bucket struct {
	tokens    float64
	burst     float64
	rate      float64 // tokens per nanosecond
	lastRefil time.Time
}

func newBucket(cfg TokenBucketConfig) *bucket {
	return &bucket{
		tokens:    float64(cfg.Burst),
		burst:     float64(cfg.Burst),
		rate:      cfg.RatePerSecond / 1e9,
		lastRefil: time.Now(),
	}
}

// tryConsume attempts to consume one token. Returns false if insufficient
// tokens. now is passed explicitly to allow deterministic testing.
func (b *bucket) tryConsume(now time.Time) bool {
	elapsed := now.Sub(b.lastRefil)
	if elapsed > 0 {
		b.tokens += float64(elapsed.Nanoseconds()) * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.lastRefil = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// refund returns one token to the bucket (used when a reserved dispatch is
// not actually dispatched, e.g. no matching executor).
func (b *bucket) refund() {
	b.tokens++
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// runState tracks per-run DRR deficit and active count.
type runState struct {
	active  int
	deficit float64
	weight  float64
}

// Gate is the admission controller. It is safe for concurrent use.
// All decision-making happens under a single mutex so that capacity
// reservation is atomic: no scope can be double-counted.
type Gate struct {
	mu sync.Mutex
	cfg Config

	// global active count
	globalActive int

	// per-run active counts and DRR state
	runs map[string]*runState

	// per-operator-key active counts
	keyActive map[string]int

	// per-provider token buckets
	buckets map[string]*bucket

	// DRR round tracking: set of run IDs seen in the current round
	roundSeen map[string]bool

	// prefetchLimit is the resolved prefetch window
	prefetchLimit int
}

const defaultPrefetchLimit = 8

// New creates a Gate from cfg. The gate is ready to use immediately.
func New(cfg Config) *Gate {
	pl := cfg.PrefetchLimit
	if pl <= 0 {
		pl = defaultPrefetchLimit
	}
	g := &Gate{
		cfg:           cfg,
		runs:          make(map[string]*runState),
		keyActive:     make(map[string]int),
		buckets:       make(map[string]*bucket),
		roundSeen:     make(map[string]bool),
		prefetchLimit: pl,
	}
	// Pre-seed provider buckets from config so they exist before any dispatch.
	for pk, bc := range cfg.ProviderRateLimits {
		g.buckets[pk] = newBucket(bc)
	}
	return g
}

// runWeight returns the DRR weight for a run, always >= 1.
func (g *Gate) runWeight(runID string) float64 {
	if g.cfg.RunWeight == nil {
		return 1
	}
	w := g.cfg.RunWeight(runID)
	if w <= 0 {
		return 1
	}
	return float64(w)
}

// providerKey derives the provider key from an executor capability name.
func (g *Gate) providerKey(executor string) string {
	if g.cfg.ProviderKeyFunc != nil {
		return g.cfg.ProviderKeyFunc(executor)
	}
	return executor
}

// getOrCreateRun returns the runState for runID, creating it if needed.
// New runs get a full weight quantum of deficit so they are immediately
// eligible for scheduling even if other runs are mid-round.
// Must be called with g.mu held.
func (g *Gate) getOrCreateRun(runID string) *runState {
	rs, ok := g.runs[runID]
	if !ok {
		w := g.runWeight(runID)
		rs = &runState{weight: w, deficit: w}
		g.runs[runID] = rs
	}
	return rs
}

// TryAdmit checks whether the dispatch described by info may be admitted given
// the current Gate state, and if so atomically reserves all required capacity.
//
// On success it returns (true, QueueReasonNotApplicable). On failure it
// returns (false, reason) where reason identifies the first violated limit.
// All capacity reserved by a successful TryAdmit must eventually be released
// via Release with the same info.
//
// workerActive and workerCap are the current active-assignment count and
// capacity for the claiming worker; these are checked but not managed by the
// Gate (the durable store owns per-worker capacity tracking).
//
// TryAdmit is concurrency-safe; it acquires and releases the gate mutex
// internally.
func (g *Gate) TryAdmit(info DispatchInfo, workerActive, workerCap int, now time.Time) (bool, QueueReason) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Worker capacity (caller-managed; Gate validates but does not track).
	if workerCap > 0 && workerActive >= workerCap {
		return false, QueueReasonWorkerFull
	}

	// 2. Global hard cap.
	if g.cfg.GlobalCap > 0 && g.globalActive >= g.cfg.GlobalCap {
		return false, QueueReasonGlobalFull
	}

	// 3. Per-run cap.
	rs := g.getOrCreateRun(info.RunID)
	if g.cfg.PerRunCap > 0 && rs.active >= g.cfg.PerRunCap {
		return false, QueueReasonRunCap
	}

	// 4. Per-operator-key cap.
	if info.OperatorKey != "" {
		cap := g.cfg.DefaultKeyCap
		if c, ok := g.cfg.PerKeyCaps[info.OperatorKey]; ok {
			cap = c
		}
		if cap > 0 && g.keyActive[info.OperatorKey] >= cap {
			return false, QueueReasonKeyCap
		}
	}

	// 5. Token-bucket rate limit for the provider.
	pk := g.providerKey(info.Executor)
	if bc, ok := g.cfg.ProviderRateLimits[pk]; ok {
		b, exists := g.buckets[pk]
		if !exists {
			b = newBucket(bc)
			g.buckets[pk] = b
		}
		if !b.tryConsume(now) {
			return false, QueueReasonRateLimit
		}
	}

	// 6. DRR fairness: only enforce when more than one run is competing.
	//    Each run gets a deficit quantum of weight per round; a run is only
	//    held back by DRR when another run still has positive deficit in the
	//    current round. If this is the only known run, skip the check so it
	//    can saturate capacity without artificial throttling.
	multipleRuns := len(g.runs) > 1
	if multipleRuns {
		if !g.roundSeen[info.RunID] {
			// New run joining an in-progress round — advance all deficits so
			// the new run is not permanently behind earlier arrivals.
			g.advanceRound()
		}
		if rs.deficit <= 0 {
			// Check if any other run still has positive deficit; if not,
			// start a new round (all runs have been serviced this round).
			anyPositive := false
			for rid, other := range g.runs {
				if rid != info.RunID && other.deficit > 0 {
					anyPositive = true
					break
				}
			}
			if anyPositive {
				// Refund the rate-limit token already consumed above.
				if pk != "" {
					if b, ok := g.buckets[pk]; ok {
						b.refund()
					}
				}
				return false, QueueReasonFairness
			}
			// All other runs are also exhausted — start a new round.
			g.advanceRound()
		}
	}

	// All checks passed — commit the reservation.
	g.globalActive++
	rs.active++
	if multipleRuns {
		rs.deficit--
		g.roundSeen[info.RunID] = true
	}
	if info.OperatorKey != "" {
		g.keyActive[info.OperatorKey]++
	}

	return true, QueueReasonNotApplicable
}

// advanceRound replenishes DRR deficits for all known runs and resets the
// round-seen set. Each run receives one weight quantum. This is called when a
// new run joins or when all runs have exhausted their deficit.
// Must be called with g.mu held.
func (g *Gate) advanceRound() {
	for _, rs := range g.runs {
		rs.deficit += rs.weight
	}
	g.roundSeen = make(map[string]bool)
}

// Release returns capacity previously reserved by a successful TryAdmit.
// It must be called exactly once for each successful TryAdmit, typically
// when the worker completes or the assignment is cancelled/expired.
func (g *Gate) Release(info DispatchInfo) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.globalActive > 0 {
		g.globalActive--
	}
	if rs, ok := g.runs[info.RunID]; ok {
		if rs.active > 0 {
			rs.active--
		}
		// Clean up idle run states to prevent unbounded map growth.
		if rs.active == 0 && rs.deficit <= 0 {
			delete(g.runs, info.RunID)
		}
	}
	if info.OperatorKey != "" {
		if g.keyActive[info.OperatorKey] > 0 {
			g.keyActive[info.OperatorKey]--
		}
	}
}

// ActiveCount returns the current number of globally active dispatches known
// to the Gate. This is informational only; the durable store is authoritative.
func (g *Gate) ActiveCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.globalActive
}

// ActiveForRun returns the active dispatch count for a specific run.
func (g *Gate) ActiveForRun(runID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rs, ok := g.runs[runID]; ok {
		return rs.active
	}
	return 0
}

// RestoreActive re-acquires capacity for dispatches that were active before
// the Gate was created (e.g. after a coordinator restart). It does not apply
// rate-limit tokens or DRR scheduling since those are stateless/approximate.
// Must be called before any TryAdmit calls to avoid under-counting.
func (g *Gate) RestoreActive(infos []DispatchInfo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, info := range infos {
		g.globalActive++
		rs := g.getOrCreateRun(info.RunID)
		rs.active++
		if info.OperatorKey != "" {
			g.keyActive[info.OperatorKey]++
		}
	}
}

// Snapshot returns a read-only view of current per-run active counts. Useful
// for UI status display and diagnostics.
func (g *Gate) Snapshot() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.runs))
	for id, rs := range g.runs {
		if rs.active > 0 {
			out[id] = rs.active
		}
	}
	return out
}
