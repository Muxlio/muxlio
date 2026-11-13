package main

import (
	"fmt"
	"os"

	"github.com/muxlio/muxlio-core/internal/authoring"
)

func cmdTemplate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio template echo|task")
		return 2
	}
	raw, err := authoring.Template(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Print(string(raw))
	return 0
}
