//go:build unix

package workers

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func processGroupHasLiveMembers(pgid int) bool {
	if runtime.GOOS != "linux" {
		return syscall.Kill(-pgid, 0) == nil
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return syscall.Kill(-pgid, 0) == nil
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		// The command name is parenthesized and may itself contain spaces.
		statText := string(stat)
		fields := strings.Fields(statText[strings.LastIndexByte(statText, ')')+1:])
		if len(fields) < 3 || fields[0] == "Z" || fields[0] == "X" {
			continue
		}
		group, err := strconv.Atoi(fields[2])
		if err == nil && group == pgid {
			return true
		}
	}
	return false
}
