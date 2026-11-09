package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateMountDirs(paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) || strings.ContainsAny(path, ":,\x00\n") {
			return fmt.Errorf("invalid sandbox mount path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("sandbox mount source is not a directory: %s", path)
		}
	}
	return nil
}
