package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type queueSample struct {
	AttemptID string    `json:"attemptId"`
	QueuedAt  time.Time `json:"queuedAt"`
	StartedAt time.Time `json:"startedAt"`
	DelayMS   float64   `json:"delayMs"`
	Cancelled bool      `json:"cancelled"`
}

// Local admission is deliberately outside the runtime. This benchmark measures
// queue time at this admission point, not remote worker/network queue time.
type measuredExecutor struct {
	inner   workers.Executor
	slots   chan struct{}
	mu      sync.Mutex
	samples []queueSample
}

func (x *measuredExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	queued := time.Now()
	cancelled := false
	select {
	case x.slots <- struct{}{}:
		defer func() { <-x.slots }()
	case <-req.Context.Done():
		cancelled = true
	}
	started := time.Now()
	// Cancellation may race a newly available slot. Never dispatch work whose
	// admission context has already ended, even if select chose the slot.
	cancelled = cancelled || req.Context.Err() != nil
	x.mu.Lock()
	x.samples = append(x.samples, queueSample{req.AttemptID, queued, started, float64(started.Sub(queued)) / float64(time.Millisecond), cancelled})
	x.mu.Unlock()
	if cancelled {
		return workers.ExecuteResult{}, req.Context.Err()
	}
	return x.inner.Execute(req)
}
func (x *measuredExecutor) snapshot() []queueSample {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]queueSample{}, x.samples...)
}

type observation struct {
	SchemaVersion              int                `json:"schemaVersion"`
	Case                       string             `json:"case"`
	Repetition                 int                `json:"repetition"`
	StartedAt                  time.Time          `json:"startedAt"`
	WallTimeMS                 float64            `json:"wallTimeMs"`
	Queue                      []queueSample      `json:"queue"`
	RecoveryCorrect            *bool              `json:"recoveryCorrect"`
	CancellationCorrect        *bool              `json:"cancellationCorrect"`
	ManualInterventions        int                `json:"manualInterventions"`
	ManualInterventionRequired bool               `json:"manualInterventionRequired"`
	Fault                      string             `json:"fault,omitempty"`
	Correct                    bool               `json:"correct"`
	Error                      string             `json:"error,omitempty"`
	Run                        *state.Run         `json:"run"`
	Usage                      state.UsageSummary `json:"usage"`
}

func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func outputText(run *state.Run, port string) string {
	if text, ok := run.Output.(string); ok {
		return text
	}
	m, _ := run.Output.(map[string]any)
	s, _ := m[port].(string)
	return s
}

// ended indicates that this helper consumed the engine's completion result.
func waitCheckpoint(s state.Store, id string, done <-chan error) (ended bool, err error) {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return true, fmt.Errorf("run ended before checkpoint: %v", err)
		case <-deadline.C:
			return false, fmt.Errorf("checkpoint timeout")
		case <-tick.C:
			a, e := s.GetActivation(id + ":checkpoint")
			if e == nil && a.State == plan.ActWaiting {
				return false, nil
			}
		}
	}
}

// verifyAuthored executes an agent-produced plan with a strict structural-only
// allowlist. Its result is checked independently against the complete corpus.
func verifyAuthored(run *state.Run, dir string) error {
	out, ok := run.Output.(map[string]any)
	if !ok {
		return errors.New("missing author output")
	}
	file := out // Single root output is unwrapped by the engine.
	if nested, ok := out["workflow"].(map[string]any); ok {
		file = nested
	}
	data, ok := file["data"].(string)
	if !ok {
		return errors.New("missing workflow bytes")
	}
	if err := os.WriteFile(filepath.Join(dir, "authored.plan.json"), []byte(data), 0600); err != nil {
		return err
	}
	var p plan.Plan
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return err
	}
	if len(p.Subgraphs) > 4 {
		return errors.New("authored plan exceeds graph bound")
	}
	for _, sg := range p.Subgraphs {
		if len(sg.Nodes) > 4 {
			return errors.New("authored plan exceeds node bound")
		}
		for _, n := range sg.Nodes {
			if n.Kind != plan.KindMap && n.Kind != plan.KindProject {
				return fmt.Errorf("authored plan uses disallowed kind %q", n.Kind)
			}
			if n.Kind == plan.KindMap && (n.MaxItems == nil || *n.MaxItems > 12 || n.Concurrency == nil || *n.Concurrency > 4) {
				return errors.New("authored map must be bounded")
			}
			if n.BodyRef == p.Root {
				return errors.New("authored plan cannot recurse")
			}
		}
	}
	// Reject cycles across map bodies before running generated control flow.
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(name string) error {
		if visiting[name] {
			return errors.New("authored map graph cannot recurse")
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, node := range p.Subgraphs[name].Nodes {
			if node.Kind == plan.KindMap {
				if err := walk(node.BodyRef); err != nil {
					return err
				}
			}
		}
		visiting[name] = false
		visited[name] = true
		return nil
	}
	for name := range p.Subgraphs {
		if err := walk(name); err != nil {
			return err
		}
	}
	if err := p.VerifyDigest(); err != nil {
		return err
	}
	deadline := 5.0
	p.DeadlineSeconds = &deadline
	p.Digest = ""
	// Exercise unseen inputs as well, so an authored constant output cannot pass.
	for index, input := range [][]string{corpus, {}, {"unseen incident record"}} {
		engine := plan.NewEngine(&p, nil)
		id, err := engine.CreateRun(raw(input))
		if err != nil {
			return err
		}
		if err = engine.Run(id); err != nil {
			return err
		}
		result, err := engine.Store().GetRun(id)
		if err != nil {
			return err
		}
		events, err := engine.Store().GetEvents(id, 0, 100000)
		if err != nil {
			return err
		}
		if err = writeJSON(filepath.Join(dir, fmt.Sprintf("authored-verification-%d.json", index)), map[string]any{"plan": engine.Plan(), "input": input, "run": result, "events": events}); err != nil {
			return err
		}
		expected := make([]any, len(input))
		for i, record := range input {
			expected[i] = record
		}
		// Empty map results may be represented as an empty slice or a nil slice.
		actual, ok := result.Output.([]any)
		if !ok || len(actual) != len(expected) {
			return errors.New("authored workflow produced incorrect records")
		}
		for i := range expected {
			if !reflect.DeepEqual(actual[i], expected[i]) {
				return errors.New("authored workflow changed a record")
			}
		}
	}

	return nil
}

func runCase(dir, name string, p *plan.Plan, executor workers.Executor, capacity, repetition int) (obs observation, err error) {
	obs = observation{SchemaVersion: 1, Case: name, Repetition: repetition, StartedAt: time.Now()}
	if err = os.Mkdir(dir, 0700); err != nil {
		return obs, err
	}
	db := filepath.Join(dir, "state.db")
	s, err := sqlite.Open(db)
	if err != nil {
		return obs, err
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	x := &measuredExecutor{inner: executor, slots: make(chan struct{}, capacity)}
	e := plan.NewEngineWithStore(p, x, s)
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stop()
	e.Context = ctx
	input := raw(map[string]any{})
	if name == "parallel-map" {
		input = raw(corpus)
	}
	id, err := e.CreateRun(input)
	if err != nil {
		return obs, err
	}
	if err = writeJSON(filepath.Join(dir, "plan.json"), e.Plan()); err != nil {
		return obs, err
	}
	if err = writeJSON(filepath.Join(dir, "input.json"), json.RawMessage(input)); err != nil {
		return obs, err
	}
	// Always retain failed observations and raw data, even when correctness fails.
	defer func() {
		obs.WallTimeMS = float64(time.Since(obs.StartedAt)) / float64(time.Millisecond)
		obs.Queue = x.snapshot()
		if s == nil {
			obs.Error = err.Error()
			obs.Correct = false
			err = errors.Join(err, writeJSON(filepath.Join(dir, "observation.json"), obs))
			return
		}
		obs.Run, _ = s.GetRun(id)
		attempts, readErr := s.ListExecutionAttempts(id, "")
		if readErr != nil {
			err = errors.Join(err, readErr)
		}
		obs.Usage = state.SummarizeUsage(attempts)
		events, readErr := s.GetEvents(id, 0, 1000000)
		if readErr != nil {
			err = errors.Join(err, readErr)
		}
		acts, readErr := s.ListActivations(id)
		if readErr != nil {
			err = errors.Join(err, readErr)
		}
		for _, a := range attempts {
			if a.AwaitReconciliationReason != "" {
				obs.ManualInterventionRequired = true
			}
		}
		if obs.Run != nil && strings.Contains(obs.Run.Error, "manual reconciliation") {
			obs.ManualInterventionRequired = true
		}
		if err != nil {
			obs.Error = err.Error()
			obs.Correct = false
		}
		for filename, value := range map[string]any{"attempts.json": attempts, "events.json": events, "activations.json": acts, "observation.json": obs} {
			err = errors.Join(err, writeJSON(filepath.Join(dir, filename), value))
		}
	}()
	start := func(engine *plan.Engine) chan error {
		done := make(chan error, 1)
		go func() { done <- engine.Run(id) }()
		return done
	}
	done := start(e)
	if name == "cancellation" || name == "recovery" {
		var ended bool
		if ended, err = waitCheckpoint(s, id, done); err != nil {
			e.Cancel()
			if !ended {
				<-done
			}
			return obs, err
		}
		if name == "cancellation" {
			obs.Fault = "explicit cancellation while waiting after completed triage"
			e.Cancel()
			runErr := <-done
			run, getErr := s.GetRun(id)
			if getErr != nil {
				return obs, getErr
			}
			attempts, getErr := s.ListExecutionAttempts(id, "")
			if getErr != nil {
				return obs, getErr
			}
			correct := errors.Is(runErr, context.Canceled) && run.State == plan.RunCancelled && len(attempts) == 1
			obs.CancellationCorrect = &correct
			obs.Correct = correct
			if !correct {
				return obs, fmt.Errorf("cancellation did not fence downstream report")
			}
			return obs, nil
		}
		obs.Fault = "cooperative engine shutdown at durable wait; SQLite close/reopen; RecoverRuns; automated signal"
		stop()
		<-done
		before, getErr := s.ListExecutionAttempts(id, "")
		if getErr != nil {
			return obs, getErr
		}
		if err = writeJSON(filepath.Join(dir, "before-recovery-attempts.json"), before); err != nil {
			return obs, err
		}
		if err = s.Close(); err != nil {
			return obs, err
		}
		s, err = sqlite.Open(db)
		if err != nil {
			return obs, err
		}
		engines, recoverErr := plan.RecoverRuns(s, x)
		if recoverErr != nil {
			return obs, recoverErr
		}
		recovered := engines[id]
		if recovered == nil {
			return obs, errors.New("recovery omitted run")
		}
		// Harness actions are automated; they are not manual operator interventions.
		if err = s.SendSignal(id, "continue", true, "benchmark-continue"); err != nil {
			return obs, err
		}
		recoveredCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		recovered.Context = recoveredCtx
		if err = recovered.Run(id); err != nil {
			return obs, err
		}
		after, getErr := s.ListExecutionAttempts(id, "")
		if getErr != nil {
			return obs, getErr
		}
		run, getErr := s.GetRun(id)
		if getErr != nil {
			return obs, getErr
		}
		preserved := false
		if len(before) == 1 {
			for _, attempt := range after {
				if reflect.DeepEqual(attempt, before[0]) {
					preserved = true
				}
			}
		}
		correct := len(before) == 1 && len(after) == 2 && preserved && run.State == plan.RunSucceeded && outputText(run, "report") == "verified incident report"
		obs.RecoveryCorrect = &correct
		obs.Correct = correct
		if !correct {
			return obs, errors.New("recovery replayed completed work or lost output")
		}
		return obs, nil
	}
	if err = <-done; err != nil {
		return obs, err
	}
	run, err := s.GetRun(id)
	if err != nil {
		return obs, err
	}
	if run.State != plan.RunSucceeded {
		return obs, fmt.Errorf("unexpected terminal state %s", run.State)
	}
	switch name {
	case "sequential":
		obs.Correct = outputText(run, "report") == "verified incident report"
	case "coding":
		obs.Correct = outputText(run, "verification") == "verified incident parser"
	case "mixed-provider":
		obs.Correct = outputText(run, "verification") == "verified incident report"
	case "large-artifact":
		row := "INC-01|payments|critical|Checkout unavailable\n"
		data := strings.Repeat(row, (2097152+len(row)-1)/len(row))[:2097152]
		sum := sha256.Sum256([]byte(data))
		obs.Correct = outputText(run, "checksum") == hex.EncodeToString(sum[:])
	case "parallel-map":
		records, _ := run.Output.([]any)
		obs.Correct = len(records) == len(corpus)
		for i, v := range records {
			if i >= len(corpus) {
				obs.Correct = false
				break
			}
			text, _ := v.(string)
			var record map[string]string
			parts := strings.Split(corpus[i], "|")
			if json.Unmarshal([]byte(text), &record) != nil || !reflect.DeepEqual(record, map[string]string{"id": parts[0], "service": parts[1], "severity": parts[2], "title": parts[3]}) {
				obs.Correct = false
			}
		}
	case "agent-authored":
		err = verifyAuthored(run, dir)
		obs.Correct = err == nil
	}
	if !obs.Correct && err == nil {
		err = errors.New("output failed independent correctness check")
	}
	return obs, err
}

func commandOutput(program string, args ...string) string {
	b, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("unavailable (%v): %s", err, b)
	}
	return strings.TrimSpace(string(b))
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	output := flag.String("out", "", "new absolute directory for raw data (required)")
	adapters := flag.String("adapters", "", "directory of installed adapter binaries (required)")
	cases := flag.String("cases", "sequential,parallel-map,large-artifact,cancellation,recovery", "comma-separated workloads; agent cases require explicit selection")
	primary := flag.String("primary", "", "primary agent executor")
	secondary := flag.String("secondary", "", "second distinct agent executor")
	concurrency := flag.Int("concurrency", 4, "map concurrency")
	capacity := flag.Int("capacity", 2, "local executor admission capacity")
	repetitions := flag.Int("repetitions", 3, "repetitions; all samples are retained")
	metadata := flag.String("metadata", "", "JSON file with model versions, pricing basis, machine and configuration notes")
	flag.Parse()
	if !filepath.IsAbs(*output) || *adapters == "" || *capacity < 1 || *concurrency < 1 || *repetitions < 1 {
		return errors.New("require absolute --out, --adapters, and positive capacity/concurrency/repetitions")
	}
	names := strings.Split(*cases, ",")
	plans := make(map[string]*plan.Plan)
	for _, name := range names {
		if _, exists := plans[name]; exists {
			return errors.New("duplicate case")
		}
		p, err := workload(name, *concurrency, *primary, *secondary)
		if err != nil {
			return err
		}
		plans[name] = p
	}
	var extra any
	if *metadata != "" {
		data, err := os.ReadFile(*metadata)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &extra); err != nil {
			return err
		}
	}
	if *primary != "" || *secondary != "" {
		if err := validateMetadata(extra); err != nil {
			return err
		}
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		return err
	}
	adapterHashes := map[string]string{}
	adapterManifests := map[string]json.RawMessage{}
	for _, p := range plans {
		for _, sg := range p.Subgraphs {
			for _, n := range sg.Nodes {
				if n.Executor != "" {
					path := filepath.Join(*adapters, strings.ReplaceAll(n.Executor, "/", "-"))
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					h := sha256.Sum256(data)
					adapterHashes[n.Executor] = hex.EncodeToString(h[:])
					contractBytes, readErr := os.ReadFile(path + ".json")
					if readErr != nil {
						return fmt.Errorf("adapter manifest required: %w", readErr)
					}
					adapterManifests[n.Executor] = contractBytes
				}
			}
		}
	}
	sources := map[string]string{}
	sourceBytes := map[string]string{}
	for _, file := range []string{"main.go", "workloads.go"} {
		data, err := os.ReadFile(filepath.Join("benchmarks", file))
		if err != nil {
			return fmt.Errorf("run from muxlio-core: %w", err)
		}
		h := sha256.Sum256(data)
		sources[file] = hex.EncodeToString(h[:])
		sourceBytes[file] = string(data)
	}
	manifest := map[string]any{"schemaVersion": 1, "suite": "muxlio-reference/v1", "createdAt": time.Now().UTC(), "argv": os.Args, "revision": commandOutput("git", "rev-parse", "HEAD"), "gitStatus": commandOutput("git", "status", "--porcelain"), "sourceHashes": sources, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "node": commandOutput("node", "--version"), "host": commandOutput("uname", "-a"), "logicalCPUs": runtime.NumCPU(), "capacity": *capacity, "mapConcurrency": *concurrency, "repetitions": *repetitions, "cases": names, "primary": *primary, "secondary": *secondary, "metadata": extra, "adapterHashes": adapterHashes, "adapterManifests": adapterManifests, "queueDefinition": "local admission: Execute entry to slot acquisition; excludes interpreter dependency waits and remote dispatch", "costBasis": "adapter-reported observed and estimated USD; unavailable stays unavailable; excludes host/subscription costs"}
	if err := writeJSON(filepath.Join(*output, "sources.json"), sourceBytes); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*output, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*output, "corpus.json"), corpus); err != nil {
		return err
	}
	allow := true
	configs := map[string]workers.ExecutorConfig{"command": {Allow: &allow}}
	for _, name := range []string{*primary, *secondary} {
		if name != "" {
			configs[name] = workers.ExecutorConfig{Allow: &allow}
		}
	}
	var failures []error
	for repetition := 1; repetition <= *repetitions; repetition++ {
		for _, name := range names {
			dir := filepath.Join(*output, fmt.Sprintf("%s-%03d", name, repetition))
			script := workers.NewScriptExecutor(*adapters, filepath.Join(dir, "work"))
			script.Executors = configs
			script.Timeout = 90 * time.Second
			obs, err := runCase(dir, name, plans[name], script, *capacity, repetition)
			fmt.Printf("%s repetition=%d correct=%t wall_ms=%.3f\n", name, repetition, obs.Correct, obs.WallTimeMS)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s/%d: %w", name, repetition, err))
			}
		}
	}
	return errors.Join(failures...)
}

func validateMetadata(value any) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return errors.New("agent runs require --metadata object recording models, versions, pricingBasis and machine")
	}
	for _, key := range []string{"models", "versions", "pricingBasis", "machine"} {
		text, ok := fields[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("metadata requires nonempty string %q", key)
		}
	}
	return nil
}
