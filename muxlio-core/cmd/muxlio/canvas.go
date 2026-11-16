package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/muxlio/muxlio-core/internal/canvas"
)

func cmdCanvas(args []string, globalJSON bool) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio canvas catalog [--json] | muxlio canvas validate <file.canvas.json> [--json]")
		return 2
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "catalog":
		fs := flag.NewFlagSet("canvas catalog", flag.ContinueOnError)
		jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON")
		if err := fs.Parse(reorderArgs(subargs)); err != nil {
			return 2
		}
		cat := canvas.Catalog()
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(cat); err != nil {
				fmt.Fprintf(os.Stderr, "failed to encode canvas catalog: %v\n", err)
				return 1
			}
			return 0
		}

		fmt.Println("Muxlio Canvas Component Catalog (v1):")
		currentCategory := ""
		for _, comp := range cat {
			if comp.Category != currentCategory {
				currentCategory = comp.Category
				fmt.Printf("\n[%s]\n", currentCategory)
			}
			fmt.Printf("  • %-14s %s\n", comp.Type, comp.Description)
		}
		fmt.Printf("\nTotal: %d approved component types.\n", len(cat))
		return 0

	case "validate":
		fs := flag.NewFlagSet("canvas validate", flag.ContinueOnError)
		jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON")
		if err := fs.Parse(reorderArgs(subargs)); err != nil {
			return 2
		}
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: muxlio canvas validate <file.canvas.json> [--json]")
			return 2
		}
		filePath := fs.Arg(0)
		data, err := os.ReadFile(filePath)
		if err != nil {
			if *jsonOut {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"valid": false,
					"file":  filePath,
					"error": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "%s: failed to read file: %v\n", filePath, err)
			}
			return 1
		}

		res, valErr := canvas.Validate(data)
		if res == nil {
			fmt.Fprintf(os.Stderr, "%s: canvas validation unavailable: %v\n", filePath, valErr)
			return 1
		}
		if valErr != nil || !res.Valid {
			if *jsonOut {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"valid":  false,
					"file":   filePath,
					"errors": res.Errors,
				})
			} else {
				fmt.Fprintf(os.Stderr, "%s: invalid Muxlio Canvas document:\n", filePath)
				for _, e := range res.Errors {
					fmt.Fprintf(os.Stderr, "  - %s\n", e)
				}
			}
			return 1
		}

		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"valid":      true,
				"file":       filePath,
				"components": res.Components,
				"depth":      res.Depth,
			})
		} else {
			fmt.Printf("%s: valid Muxlio Canvas v1 document (%d components, depth %d)\n", filePath, res.Components, res.Depth)
		}
		return 0

	default:
		fmt.Fprintf(os.Stderr, "unknown canvas command %q. Available: catalog, validate\n", subcmd)
		return 2
	}
}
