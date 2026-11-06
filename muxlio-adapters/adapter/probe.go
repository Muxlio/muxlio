package adapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/muxlio/muxlio-adapters/contract"
)

const ProbeFlag = "--muxlio-probe"

func ProbeRequested(args []string) bool { return len(args) == 2 && args[1] == ProbeFlag }

func RunProbe(checks ...contract.ProbeCheck) {
	result := contract.ProbeResult{Ready: true}
	for _, c := range checks {
		result.Add(c)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	os.Exit(0)
}

// CommandCheck runs only adapter-owned, read-only commands. Output is discarded:
// CLI diagnostics can contain credentials, account identifiers or environment values.
func CommandCheck(category, binary string, args ...string) contract.ProbeCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = CleanEnvForChild()
	var output probeOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = time.Second
	ok := cmd.Run() == nil && ctx.Err() == nil && !output.overflow && len(strings.TrimSpace(string(output.data))) > 0
	version := ""
	if category == contract.ProbeVersion {
		version = observedRunnerVersion(string(output.data))
		ok = ok && version != ""
	}
	if category == contract.ProbeAuthentication {
		diagnostic := strings.ToLower(string(output.data))
		if regexp.MustCompile(`"loggedin"\s*:\s*false`).MatchString(diagnostic) {
			ok = false
		}
		for _, denied := range []string{"not logged in", "logged out", "unauthenticated", `"loggedin":false`, `"loggedin": false`} {
			if strings.Contains(diagnostic, denied) {
				ok = false
			}
		}
	}
	message := category + " verified"
	if !ok {
		message = category + " verification failed; check runner installation and login"
	}
	check := contract.ProbeCheck{Name: category, Passed: ok, Message: message}
	if category == contract.ProbeVersion && ok {
		check.Runner = "runner"
		check.Version = version
	}
	return check
}
func BinaryCheck(binary string) contract.ProbeCheck {
	_, err := exec.LookPath(binary)
	return contract.ProbeCheck{Name: contract.ProbeBinary, Passed: err == nil, Message: "runner executable must be available"}
}

// RunnerChecks never starts a generation, login flow or billable task.
func RunnerChecks(binary string, authArgs ...string) []contract.ProbeCheck {
	return []contract.ProbeCheck{
		BinaryCheck(binary), CommandCheck(contract.ProbeVersion, binary, "--version"),
		CommandCheck(contract.ProbeAuthentication, binary, authArgs...),
		{Name: contract.ProbeBackend, Passed: true, Message: "native execution backend"},
		{Name: contract.ProbeTools, Passed: true, Message: "no additional companion tools required"},
	}
}

// Probe output is inspected locally and never serialized or logged.
type probeOutput struct {
	data     []byte
	overflow bool
}

func (b *probeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (16 << 10) - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// CheckRunnerEnvironment re-observes the adapter-owned runner before generation.
func CheckRunnerEnvironment(req *Request, binary string) error {
	if req.Environment == nil {
		return nil
	}
	check := CommandCheck(contract.ProbeVersion, binary, "--version")
	report := contract.ProbeResult{Checks: []contract.ProbeCheck{check}}
	return req.Environment.CheckRunners(report.RunnerVersions())
}

// Inspect whole version tokens: matching a prefix can hide a changed build or
// turn an unsupported four-component version into a compatible three-part pin.
func observedRunnerVersion(output string) string {
	for _, token := range strings.FieldsFunc(output, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '+' && r != '-'
	}) {
		token = strings.TrimPrefix(token, "v")
		if contract.IsRunnerVersion(token) {
			return token
		}
	}
	return ""
}
