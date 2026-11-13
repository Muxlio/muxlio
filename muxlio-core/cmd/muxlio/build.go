package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-core/internal/authoring"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/plan"
)

// runWorkflowTests looks for a co-located *.test.mjs (or *.test.ts) file next
// to the workflow source and runs it with `node --import tsx --test`. Returns
// "" on success, an error description on failure. Silently skips when no test
// file is found or node/tsx is unavailable.
func runWorkflowTests(workflowPath string, jsonOut bool) string {
	dir := filepath.Dir(workflowPath)
	base := strings.TrimSuffix(filepath.Base(workflowPath), filepath.Ext(workflowPath))
	candidates := []string{
		filepath.Join(dir, base+".test.mjs"),
		filepath.Join(dir, base+".test.ts"),
		filepath.Join(dir, base+".test.js"),
	}
	var testFile string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			testFile = c
			break
		}
	}
	if testFile == "" {
		return "" // no test file — skip silently
	}

	cmd := exec.Command("node", "--import", "tsx", "--test", testFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MUXLIO_COMPILE=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("workflow tests failed (%s):\n%s", testFile, out.String())
	}
	if !jsonOut {
		fmt.Printf("  ✓ workflow tests passed (%s)\n", filepath.Base(testFile))
	}
	return ""
}

func cmdBuild(args []string, globalJSON bool) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	outPath := fs.String("out", "", "output file path for compiled plan.json")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio build <workflow.ts|workflow.json> [--out plan.json] [--json]")
		return 2
	}

	sourcePath := fs.Arg(0)
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		emitDiagnostic(sourcePath, err, *jsonOut)
		return 1
	}

	ext := strings.ToLower(filepath.Ext(absSource))
	var rawPlan []byte

	switch ext {
	case ".json":
		data, err := os.ReadFile(absSource)
		if err != nil {
			emitDiagnostic(sourcePath, err, *jsonOut)
			return 1
		}
		rawPlan = data

	case ".ts", ".js", ".mjs":
		compiled, err := compileTypeScriptWorkflow(absSource)
		if err != nil {
			emitDiagnostic(sourcePath, err, *jsonOut)
			return 1
		}
		rawPlan = compiled

	default:
		emitDiagnostic(sourcePath, fmt.Errorf("unsupported file extension %q (expected .ts, .js, or .json)", ext), *jsonOut)
		return 1
	}

	var p plan.Plan
	if err := json.Unmarshal(rawPlan, &p); err != nil {
		emitDiagnostic(sourcePath, err, *jsonOut)
		return 1
	}

	// Validate the compiled plan
	issues := plan.Validate(&p)
	verrs := plan.ErrorsOnly(issues)
	warnings := plan.WarningsOnly(issues)
	for i := range verrs {
		if verrs[i].Source != nil && ext == ".json" {
			verrs[i].Source.File = sourcePath
		}
	}
	if len(verrs) > 0 {
		if *jsonOut {
			out, _ := diagnostic.JSON(map[string]any{
				"valid":    false,
				"errors":   verrs,
				"warnings": warnings,
				"digest":   p.Digest,
			})
			fmt.Println(string(out))
		} else {
			fmt.Fprintf(os.Stderr, "✗ plan has %d validation error(s):\n", len(verrs))
			for _, ve := range verrs {
				loc := ""
				if ve.Subgraph != "" {
					loc = " [" + ve.Subgraph
					if ve.NodeID != "" {
						loc += "/" + ve.NodeID
					}
					loc += "]"
				}
				fmt.Fprintf(os.Stderr, "  %s: %s%s\n", ve.Code, ve.Message, loc)
				if ve.Fix != "" {
					fmt.Fprintf(os.Stderr, "    fix: %s\n", ve.Fix)
				}
			}
		}
		return 1
	}

	// Compute canonical digest if missing or ensure update
	formatted, err := json.MarshalIndent(&p, "", "  ")
	if err != nil {
		emitDiagnostic(sourcePath, err, *jsonOut)
		return 1
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, formatted, 0o644); err != nil {
			emitDiagnostic(sourcePath, err, *jsonOut)
			return 1
		}
	}

	if *jsonOut {
		safeWarnings, _ := diagnostic.JSON(warnings)
		res := map[string]any{
			"warnings":  json.RawMessage(safeWarnings),
			"valid":     true,
			"name":      p.Name,
			"irVersion": p.IRVersion,
			"digest":    p.Digest,
			"subgraphs": len(p.Subgraphs),
			"plan":      &p,
		}
		if *outPath != "" {
			res["output"] = *outPath
		}
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	if *outPath != "" {
		fmt.Printf("✓ Compiled %q -> %s (digest=%s, subgraphs=%d)\n", p.Name, *outPath, p.Digest, len(p.Subgraphs))
	} else {
		fmt.Println(string(formatted))
	}
	return 0
}

func compileTypeScriptWorkflow(filePath string) ([]byte, error) {
	return authoring.Compile(filePath)
}
