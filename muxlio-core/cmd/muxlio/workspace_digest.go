package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func cmdWorkspace(args []string) int {
	if len(args) != 2 || args[0] != "digest" {
		fmt.Fprintln(os.Stderr, "usage: muxlio workspace digest <prepared-directory>")
		return 2
	}
	path, err := filepath.Abs(args[1])
	if err == nil {
		var digest string
		digest, err = workers.WorkspaceDigest(path)
		if err == nil {
			fmt.Println(digest)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
