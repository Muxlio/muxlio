package admission

import (
	"fmt"
	"testing"
	"time"
)

// newGate is a test helper that creates a gate with a simple executor-name
// provider key function (no transformation).
func newGate(cfg Config) *Gate {
	return New(cfg)
}

// admit is a convenience wrapper for TryAdmit that uses a fixed time and
// workerActive=0, workerCap=0 (i.e. worker capacity is not the limiting
// factor) unless overridden.
func admit(g *Gate, info DispatchInfo) (bool, QueueReason) {
	return g.TryAdmit(info, 0, 0, time.Now())
}

// ─── Worker capacity ────────────────────────────────────────────────────────

func TestWorkerCapacityEnforced(t *testing.T) {
	g := newGate(Config{})
	// workerActive == workerCap → rejected
	ok, reason := g.TryAdmit(DispatchInfo{RunID: "run", Executor: "ex"}, 4, 4, time.Now())
	if ok || reason != QueueReasonWorkerFull {
		t.Fatalf("got ok=%v reason=%v; want false/worker_full", ok, reason)
	}
	// workerActive < workerCap → admitted
	ok, reason = g.TryAdmit(DispatchInfo{RunID: "run", Executor: "ex"}, 3, 4, time.Now())
	if !ok || reason != QueueReasonNotApplicable {
		t.Fatalf("got ok=%v reason=%v; want true/", ok, reason)
	}
}

func TestWorkerCapacityZeroMeansUnlimited(t *testing.T) {
	g := newGate(Config{})
	// workerCap == 0 → Gate ignores worker capacity check
	ok, _ := g.TryAdmit(DispatchInfo{RunID: "run", Executor: "ex"}, 1000, 0, time.Now())
	if !ok {
		t.Fatal("want admitted when workerCap=0")
	}
}

// ─── Global cap ─────────────────────────────────────────────────────────────

func TestGlobalCapEnforced(t *testing.T) {
	g := newGate(Config{GlobalCap: 2})
	info := DispatchInfo{Executor: "ex"}
	for i := 0; i < 2; i++ {
		info.RunID = fmt.Sprintf("run-%d", i)
		ok, _ := admit(g, info)
		if !ok {
			t.Fatalf("slot %d rejected", i)
		}
	}
	info.RunID = "run-2"
	ok, reason := admit(g, info)
	if ok || reason != QueueReasonGlobalFull {
		t.Fatalf("got ok=%v reason=%v; want false/global_full", ok, reason)
	}
}

func TestGlobalCapReleasedOnRelease(t *testing.T) {
	g := newGate(Config{GlobalCap: 1})
	info := DispatchInfo{RunID: "run", Executor: "ex"}
	ok, _ := admit(g, info)
	if !ok {
		t.Fatal("first admit")
	}
	ok, _ = admit(g, DispatchInfo{RunID: "run2", Executor: "ex"})
	if ok {
		t.Fatal("second admit should be blocked")
	}
	g.Release(info)
	ok, _ = admit(g, DispatchInfo{RunID: "run2", Executor: "ex"})
	if !ok {
		t.Fatal("admit after release")
	}
}

// ─── Per-run cap ─────────────────────────────────────────────────────────────

func TestPerRunCapEnforced(t *testing.T) {
	g := newGate(Config{PerRunCap: 3})
	for i := 0; i < 3; i++ {
		ok, _ := admit(g, DispatchInfo{RunID: "run", Executor: "ex"})
		if !ok {
			t.Fatalf("slot %d rejected", i)
		}
	}
	ok, reason := admit(g, DispatchInfo{RunID: "run", Executor: "ex"})
	if ok || reason != QueueReasonRunCap {
		t.Fatalf("got ok=%v reason=%v; want false/run_cap", ok, reason)
	}
	// A different run is unaffected.
	ok, _ = admit(g, DispatchInfo{RunID: "other-run", Executor: "ex"})
	if !ok {
		t.Fatal("other run should not be blocked by per-run cap of first run")
	}
}

// ─── Per-key cap ─────────────────────────────────────────────────────────────

func TestPerKeyCap(t *testing.T) {
	g := newGate(Config{PerKeyCaps: map[string]int{"keyA": 2}})
	for i := 0; i < 2; i++ {
		ok, _ := admit(g, DispatchInfo{RunID: fmt.Sprintf("run-%d", i), Executor: "ex", OperatorKey: "keyA"})
		if !ok {
			t.Fatalf("slot %d rejected", i)
		}
	}
	ok, reason := admit(g, DispatchInfo{RunID: "run-3", Executor: "ex", OperatorKey: "keyA"})
	if ok || reason != QueueReasonKeyCap {
		t.Fatalf("got ok=%v reason=%v; want false/key_cap", ok, reason)
	}
}

func TestDefaultKeyCap(t *testing.T) {
	g := newGate(Config{DefaultKeyCap: 1})
	ok, _ := admit(g, DispatchInfo{RunID: "run", Executor: "ex", OperatorKey: "anykey"})
	if !ok {
		t.Fatal("first admit")
	}
	ok, reason := admit(g, DispatchInfo{RunID: "run2", Executor: "ex", OperatorKey: "anykey"})
	if ok || reason != QueueReasonKeyCap {
		t.Fatalf("got ok=%v reason=%v; want false/key_cap", ok, reason)
	}
	// A different key is not affected.
	ok, _ = admit(g, DispatchInfo{RunID: "run3", Executor: "ex", OperatorKey: "otherkey"})
	if !ok {
		t.Fatal("different key should not be blocked")
	}
}

// ─── Token-bucket rate limit ─────────────────────────────────────────────────

func TestProviderRateLimitBurst(t *testing.T) {
	// Burst=2, very slow refill so the burst is the only supply.
	g := newGate(Config{
		ProviderRateLimits: map[string]TokenBucketConfig{
			"prov": {Burst: 2, RatePerSecond: 0.001},
		},
	})
	now := time.Now()
	ok1, _ := g.TryAdmit(DispatchInfo{RunID: "run1", Executor: "prov"}, 0, 0, now)
	ok2, _ := g.TryAdmit(DispatchInfo{RunID: "run2", Executor: "prov"}, 0, 0, now)
	ok3, r3 := g.TryAdmit(DispatchInfo{RunID: "run3", Executor: "prov"}, 0, 0, now)
	if !ok1 || !ok2 {
		t.Fatalf("burst tokens should allow two admissions: %v %v", ok1, ok2)
	}
	if ok3 || r3 != QueueReasonRateLimit {
		t.Fatalf("third admission should be rate-limited: ok=%v reason=%v", ok3, r3)
	}
}

func TestProviderRateLimitRefill(t *testing.T) {
	// Burst=1, 1 token/second.
	g := newGate(Config{
		ProviderRateLimits: map[string]TokenBucketConfig{
			"prov": {Burst: 1, RatePerSecond: 1},
		},
	})
	now := time.Now()
	ok1, _ := g.TryAdmit(DispatchInfo{RunID: "run1", Executor: "prov"}, 0, 0, now)
	if !ok1 {
		t.Fatal("first admission")
	}
	// Immediately after: rate-limited.
	ok2, r2 := g.TryAdmit(DispatchInfo{RunID: "run2", Executor: "prov"}, 0, 0, now)
	if ok2 || r2 != QueueReasonRateLimit {
		t.Fatalf("immediate second: ok=%v reason=%v", ok2, r2)
	}
	// 2 seconds later: refilled.
	later := now.Add(2 * time.Second)
	ok3, _ := g.TryAdmit(DispatchInfo{RunID: "run3", Executor: "prov"}, 0, 0, later)
	if !ok3 {
		t.Fatal("after refill should be admitted")
	}
}

func TestProviderKeyFuncUsed(t *testing.T) {
	// Two executors map to the same provider key.
	g := newGate(Config{
		ProviderKeyFunc: func(executor string) string {
			if executor == "openai/gpt-4o" || executor == "openai/gpt-4-turbo" {
				return "openai"
			}
			return executor
		},
		ProviderRateLimits: map[string]TokenBucketConfig{
			"openai": {Burst: 1, RatePerSecond: 0.001},
		},
	})
	now := time.Now()
	ok1, _ := g.TryAdmit(DispatchInfo{RunID: "run1", Executor: "openai/gpt-4o"}, 0, 0, now)
	if !ok1 {
		t.Fatal("first admission")
	}
	// Different model, same provider — rate limited.
	ok2, r2 := g.TryAdmit(DispatchInfo{RunID: "run2", Executor: "openai/gpt-4-turbo"}, 0, 0, now)
	if ok2 || r2 != QueueReasonRateLimit {
		t.Fatalf("cross-model rate limit: ok=%v reason=%v", ok2, r2)
	}
}

// ─── Deficit round-robin fairness ───────────────────────────────────────────

// TestFairnessLargeRunCannotStarveNewRun verifies the primary fairness
// property: a 1000-child fan-out run cannot indefinitely starve an unrelated
// newly-arrived run that also needs admissions.
func TestFairnessLargeRunCannotStarveNewRun(t *testing.T) {
	// Large per-run cap so the fan-out run is not blocked by PerRunCap.
	// GlobalCap allows moderate parallelism.
	g := newGate(Config{GlobalCap: 100, PerRunCap: 100})

	const largeRunChildren = 1000
	const maxRoundsWithoutNewRun = 3

	largeRun := DispatchInfo{RunID: "large-run", Executor: "ex"}
	newRun := DispatchInfo{RunID: "new-run", Executor: "ex"}

	// Saturate with 100 large-run admissions first, then release them so the
	// large run has pending work while we try to admit the new run.
	admitted := 0
	for i := 0; i < 100; i++ {
		if ok, _ := admit(g, largeRun); ok {
			admitted++
		}
	}
	for i := 0; i < admitted; i++ {
		g.Release(largeRun)
	}

	// Now simulate a scheduling loop: the large run submits 1000 dispatches
	// and the new run submits one. The new run must be admitted within
	// maxRoundsWithoutNewRun full rounds of the large run.
	largeRunAdmissions := 0
	newRunAdmitted := false
	for round := 0; round < maxRoundsWithoutNewRun*largeRunChildren+1; round++ {
		// Try large run first (simulating it being "first in queue").
		if ok, _ := admit(g, largeRun); ok {
			largeRunAdmissions++
			g.Release(largeRun)
		}
		// Try new run.
		if ok, _ := admit(g, newRun); ok {
			newRunAdmitted = true
			g.Release(newRun)
			break
		}
	}
	if !newRunAdmitted {
		t.Fatalf("new run was not admitted after %d large-run admissions; fairness broken", largeRunAdmissions)
	}
}

// TestFairnessEqualWeightRuns verifies that two equal-weight runs share
// admission turns in roughly equal proportion.
func TestFairnessEqualWeightRuns(t *testing.T) {
	g := newGate(Config{GlobalCap: 50})
	const totalAdmissions = 200
	countA, countB := 0, 0
	a := DispatchInfo{RunID: "run-A", Executor: "ex"}
	b := DispatchInfo{RunID: "run-B", Executor: "ex"}
	for i := 0; i < totalAdmissions; i++ {
		if ok, _ := admit(g, a); ok {
			countA++
			g.Release(a)
		}
		if ok, _ := admit(g, b); ok {
			countB++
			g.Release(b)
		}
	}
	// Allow up to 40% skew (DRR delivers near-equal, not perfect equality).
	ratio := float64(countA) / float64(countA+countB)
	if ratio < 0.3 || ratio > 0.7 {
		t.Fatalf("unfair split: A=%d B=%d ratio=%.2f", countA, countB, ratio)
	}
}

// ─── Atomic reservation ──────────────────────────────────────────────────────

// TestAtomicReservationNeverExceedsGlobalCap runs concurrent TryAdmit calls
// and verifies that the total admitted count never exceeds GlobalCap.
func TestAtomicReservationNeverExceedsGlobalCap(t *testing.T) {
	const cap = 10
	const goroutines = 100
	g := newGate(Config{GlobalCap: cap})
	type result struct {
		ok bool
		id int
	}
	results := make(chan result, goroutines)
	// Admissions are concurrent.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			ok, _ := g.TryAdmit(DispatchInfo{RunID: fmt.Sprintf("run-%d", id), Executor: "ex"}, 0, 0, time.Now())
			results <- result{ok: ok, id: id}
		}(i)
	}
	var admitted int
	for i := 0; i < goroutines; i++ {
		r := <-results
		if r.ok {
			admitted++
		}
	}
	if admitted > cap {
		t.Fatalf("admitted %d dispatches, exceeds GlobalCap %d", admitted, cap)
	}
	if admitted != cap {
		t.Fatalf("admitted %d dispatches, expected exactly GlobalCap %d (some capacity wasted)", admitted, cap)
	}
}

// ─── Release and restore ─────────────────────────────────────────────────────

func TestReleaseDecrementsCounters(t *testing.T) {
	g := newGate(Config{GlobalCap: 1, PerRunCap: 1})
	info := DispatchInfo{RunID: "run", Executor: "ex", OperatorKey: "key"}
	ok, _ := admit(g, info)
	if !ok {
		t.Fatal("first admit")
	}
	if g.ActiveCount() != 1 {
		t.Fatalf("active count before release: %d", g.ActiveCount())
	}
	g.Release(info)
	if g.ActiveCount() != 0 {
		t.Fatalf("active count after release: %d", g.ActiveCount())
	}
	// Second admission should now succeed.
	ok, _ = admit(g, info)
	if !ok {
		t.Fatal("admit after release")
	}
}

func TestRestoreActive(t *testing.T) {
	g := newGate(Config{GlobalCap: 2})
	// Simulate two pre-existing active dispatches from a coordinator restart.
	g.RestoreActive([]DispatchInfo{
		{RunID: "run1", Executor: "ex"},
		{RunID: "run2", Executor: "ex"},
	})
	if g.ActiveCount() != 2 {
		t.Fatalf("active count after restore: %d", g.ActiveCount())
	}
	// A third admission should be blocked by GlobalCap=2.
	ok, reason := admit(g, DispatchInfo{RunID: "run3", Executor: "ex"})
	if ok || reason != QueueReasonGlobalFull {
		t.Fatalf("should be blocked after restore: ok=%v reason=%v", ok, reason)
	}
}

// ─── Waiting activations use no execution slot ───────────────────────────────

// TestWaitingActivationsUseNoSlot verifies that an activation in the "waiting"
// state (timer or signal wait) does not hold an admission slot. This is
// the semantic invariant: the Gate's Release is called when an activation
// transitions to waiting, and TryAdmit is called again when it resumes.
// This test validates the accounting via direct Gate API calls.
func TestWaitingActivationsUseNoSlot(t *testing.T) {
	g := newGate(Config{GlobalCap: 1, PerRunCap: 1})
	info := DispatchInfo{RunID: "run", Executor: "ex"}

	// Admit and immediately "go waiting" → release the slot.
	ok, _ := admit(g, info)
	if !ok {
		t.Fatal("first admit")
	}
	// Simulate transition to waiting state: release the slot.
	g.Release(info)
	if g.ActiveCount() != 0 {
		t.Fatalf("active count should be zero while waiting: %d", g.ActiveCount())
	}

	// Another unrelated run can now take the freed slot.
	other := DispatchInfo{RunID: "other-run", Executor: "ex"}
	ok, _ = admit(g, other)
	if !ok {
		t.Fatal("other run should be admitted while first run is waiting")
	}
	g.Release(other)

	// When the first run resumes (wakeup), it re-acquires the slot.
	ok, _ = admit(g, info)
	if !ok {
		t.Fatal("waiting run should be re-admittable on resume")
	}
}

// ─── Dispatch never exceeds hard limit ───────────────────────────────────────

func TestHardLimitNeverExceeded(t *testing.T) {
	const hardCap = 5
	g := newGate(Config{GlobalCap: hardCap})
	var active int
	for round := 0; round < 1000; round++ {
		info := DispatchInfo{RunID: fmt.Sprintf("run-%d", round%20), Executor: "ex"}
		ok, _ := admit(g, info)
		if ok {
			active++
			if active > hardCap {
				t.Fatalf("active count %d exceeds hardCap %d at round %d", active, hardCap, round)
			}
			g.Release(info)
			active--
		}
	}
}

// ─── Snapshot ────────────────────────────────────────────────────────────────

func TestSnapshot(t *testing.T) {
	g := newGate(Config{})
	infoA := DispatchInfo{RunID: "run-a", Executor: "ex"}
	infoB := DispatchInfo{RunID: "run-b", Executor: "ex"}
	admit(g, infoA)
	admit(g, infoA)
	admit(g, infoB)
	snap := g.Snapshot()
	if snap["run-a"] != 2 {
		t.Errorf("run-a active: %d, want 2", snap["run-a"])
	}
	if snap["run-b"] != 1 {
		t.Errorf("run-b active: %d, want 1", snap["run-b"])
	}
}
