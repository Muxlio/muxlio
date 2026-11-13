package plan

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/workers"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalNumbersPreservePrecision(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"1.0", "1"}, {"-0.0", "0"}, {"1e20", "100000000000000000000"},
		{"1e21", "1e+21"}, {"0.0000001", "1e-7"},
		{"9007199254740993", "9007199254740993"},
		{"0.10000000000000001", "0.10000000000000001"},
		{"1e-1000000000", "1e-1000000000"},
	} {
		got, err := canonicalJSON(json.Number(tc.input))
		if err != nil || string(got) != tc.want {
			t.Errorf("canonical %s = %s, %v; want %s", tc.input, got, err, tc.want)
		}
	}
	if _, err := canonicalJSON(json.Number("1e400")); err == nil {
		t.Fatal("accepted a number outside the supported range")
	}
}

// Exercise the real SDK hasher and Go verifier, not parallel test-only hashers.
func TestTypeScriptDigestRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules/tsx")); err != nil {
		t.Skip("tsx unavailable; install workspace dependencies to run cross-language regression")
	}
	fixture := `{"irVersion":"1.0","name":"canonical <&> ","root":"root","requiredExecutors":[],"inputSchema":{"z":1e-7,"a":{"10":1e21,"2":0.000001,"😀":-0.0,"":""}},"subgraphs":{"root":{"nodes":[{"id":"step","kind":"execute","executor":"test/echo","task":"","inputs":{},"outputs":{"result":{}}}],"output":{"result":{"expr":{"op":"literal","value":{"empty":[],"false":false,"number":1e20},"index":0}}}}}}`
	command := exec.Command("node", "--import", "tsx", "--input-type=module", "-e", `import {computeDigest} from './muxlio-sdk/src/builder.ts'; let input=''; for await (const chunk of process.stdin) input+=chunk; const plan=JSON.parse(input); plan.digest=computeDigest(plan); console.log(JSON.stringify(plan));`)
	command.Dir = root
	command.Stdin = strings.NewReader(fixture)
	command.Env = append(os.Environ(), "TSX_DISABLE_CACHE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("TypeScript hasher: %v: %s", err, output)
	}
	var p Plan
	if err := json.Unmarshal(output, &p); err != nil {
		t.Fatal(err)
	}
	if err := p.VerifyDigest(); err != nil {
		t.Fatal(err)
	}
	pinned, err := json.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Plan
	if err := json.Unmarshal(pinned, &recovered); err != nil {
		t.Fatal(err)
	}
	if err := recovered.VerifyDigest(); err != nil {
		t.Fatalf("persisted plan: %v", err)
	}
	recovered.Name = "tampered"
	if err := recovered.VerifyDigest(); err == nil {
		t.Fatal("modified plan accepted stale source bytes")
	}
}

func TestAsyncCompilerPlanCreatesGoRun(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "muxlio-sdk/dist/index.js")); err != nil {
		t.Skip("run npm run build for compiler regression")
	}
	if _, err = os.Stat(filepath.Join(root, "node_modules/tsx")); err != nil {
		t.Skip("tsx unavailable")
	}
	cmd := exec.Command("node", "--import", "tsx", "--input-type=module", "-e", `import {register} from 'node:module'; register('./scripts/workspace-loader.mjs',import.meta.url); const {defineAsyncWorkflow}=await import('./muxlio-compiler/src/async.ts'); const {plan}=await defineAsyncWorkflow({name:'compiler digest regression',run:async(ctx,input)=>{const step=await ctx.execute('echo',{executor:'fake/echo',inputs:{value:input},outputs:{result:{mime:'application/json'}}});return step.outputs.result;}}); console.log(JSON.stringify(plan));`)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TSX_DISABLE_CACHE=1")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("compiler: %v", err)
	}
	var compiled Plan
	if err = json.Unmarshal(output, &compiled); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(&compiled, workers.NewFake().ByExecutor("fake/echo", workers.ExecuteResult{Outputs: map[string]any{"result": "compiled"}}))
	id, err := engine.CreateRun(json.RawMessage(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("Go rejected TS compiler plan: %v", err)
	}
	if err = engine.Run(id); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Store().GetRun(id)
	if err != nil || run.State != RunSucceeded || run.Output != "compiled" {
		t.Fatalf("run: %#v %v", run, err)
	}
}
