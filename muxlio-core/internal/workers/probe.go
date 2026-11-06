package workers

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// Probe uses the execution environment, without task input or secrets from a plan.
// Only explicitly versioned probe binaries may be invoked, so legacy executors
// cannot accidentally execute work when asked for readiness.
func (s *ScriptExecutor) Probe(ctx context.Context, name string) contract.ProbeResult {
	r := contract.ProbeResult{Ready: true}
	add := func(category string, ok bool, message string) {
		r.Add(contract.ProbeCheck{Name: category, Passed: ok, Message: message})
	}
	allowed, err := s.CanExecute(name)
	if err != nil {
		add(contract.ProbeProtocol, false, "invalid adapter descriptor or executor name")
		return r
	}
	add(contract.ProbePermission, allowed, "local executor permission required")
	if !allowed {
		return r
	}
	m, err := s.Descriptor(name)
	compatible := err == nil && m.ProbeVersion == "1" && m.ContractVersion == contract.Version
	add(contract.ProbeProtocol, compatible, "adapter must support contract and probe version 1")
	if !compatible {
		return r
	}
	env, err := s.environment(os.TempDir(), false, name)
	if err != nil {
		add(contract.ProbePermission, false, "invalid worker execution environment")
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.scriptPath(name), "--muxlio-probe")
	cmd.Env = env
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	var output probeBuffer
	cmd.Stdout = &output
	if cmd.Run() != nil || output.overflow {
		add(contract.ProbeBinary, false, "adapter probe failed or timed out")
		return r
	}
	report, err := contract.ParseProbeResult(output.data)
	if err != nil {
		add(contract.ProbeProtocol, false, "invalid adapter readiness response")
		return r
	}
	for _, c := range report.Checks {
		r.Add(c)
	}
	if !report.Ready {
		r.Ready = false
	}
	return r
}

// Bound output without retaining potentially unbounded CLI diagnostics.
type probeBuffer struct {
	data     []byte
	overflow bool
}

func (b *probeBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
