package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func cmdTest(args []string, globalJSON bool) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	inputFlag := fs.String("input", "", "input JSON file path")
	fakeConfig := fs.String("fake-config", "", "fake executor configuration JSON file path")
	_ = fs.Bool("fake", true, "run with fake executor (default true)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio test <workflow.ts|plan.json> --input <input.json> [--fake-config <fake.json>] [--json]")
		return 2
	}

	workflowPath := fs.Arg(0)
	inputPath := *inputFlag
	if inputPath == "" && fs.NArg() >= 2 {
		inputPath = fs.Arg(1)
	}
	if inputPath == "" {
		fmt.Fprintln(os.Stderr, "error: --input <input.json> is required")
		return 2
	}

	// 1. Load Plan (compiling if TS)
	var p *plan.Plan
	ext := strings.ToLower(filepath.Ext(workflowPath))
	if ext == ".ts" || ext == ".js" || ext == ".mjs" {
		absPath, err := filepath.Abs(workflowPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid path: %v\n", err)
			return 1
		}
		raw, err := compileTypeScriptWorkflow(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: compiling workflow: %v\n", err)
			return 1
		}
		var parsed plan.Plan
		if err := json.Unmarshal(raw, &parsed); err != nil {
			fmt.Fprintf(os.Stderr, "error: parsing compiled plan: %v\n", err)
			return 1
		}
		p = &parsed
	} else {
		loaded, err := loadPlan(workflowPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: loading plan: %v\n", err)
			return 1
		}
		p = loaded
	}

	// 2. Validate Plan
	verrs := plan.Validate(p)
	if len(verrs) > 0 {
		if *jsonOut {
			out, _ := json.MarshalIndent(map[string]any{
				"valid":  false,
				"errors": verrs,
				"state":  "failed",
			}, "", "  ")
			fmt.Println(string(out))
		} else {
			fmt.Fprintf(os.Stderr, "error: plan validation failed with %d error(s)\n", len(verrs))
			for _, ve := range verrs {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", ve.Code, ve.Message)
			}
		}
		return 1
	}

	// 3. Load input JSON
	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading input file %s: %v\n", inputPath, err)
		return 1
	}

	// 4. Setup Fake Executor and In-Memory Store
	fakeExec, err := loadFakeExecutor(*fakeConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading fake executor config: %v\n", err)
		return 1
	}

	memStore := state.New()
	engine := plan.NewEngineWithStore(p, fakeExec, memStore)
	ctx := context.Background()
	engine.Context = ctx

	runID, err := engine.CreateRun(inputData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating test run: %v\n", err)
		return 1
	}

	runErr := engine.Run(runID)
	run, _ := memStore.GetRun(runID)
	events, _ := memStore.GetEvents(runID, 0, 0)
	activations, _ := memStore.ListActivations(runID)

	success := runErr == nil && run != nil && run.State == "succeeded"

	if *jsonOut {
		stateStr := "failed"
		var outputVal any
		var errMsg string
		if run != nil {
			stateStr = run.State
			outputVal = run.Output
			errMsg = run.Error
		}
		res := map[string]any{
			"test":        "passed",
			"workflow":    p.Name,
			"runId":       runID,
			"state":       stateStr,
			"output":      outputVal,
			"error":       errMsg,
			"eventsCount": len(events),
			"activations": len(activations),
			"events":      formatEvents(events),
		}
		if !success {
			res["test"] = "failed"
		}
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
		if success {
			return 0
		}
		return 1
	}

	if success {
		fmt.Printf("✓ PASS: workflow %q passed (%d activations, %d events)\n", p.Name, len(activations), len(events))
		if run != nil && run.Output != nil {
			outRaw, _ := json.MarshalIndent(run.Output, "  ", "  ")
			fmt.Printf("  Output:\n  %s\n", string(outRaw))
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "✗ FAIL: workflow %q failed\n", p.Name)
	if run != nil && run.Error != "" {
		fmt.Fprintf(os.Stderr, "  Error: %s\n", run.Error)
	} else if runErr != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", runErr)
	}
	return 1
}
