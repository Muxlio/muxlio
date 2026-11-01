package main

import "strings"

// reorderArgs reorganizes command-line arguments so that flag arguments
// precede positional arguments, allowing Go's flag.FlagSet to parse flags
// even if they appear after positional arguments.
func reorderArgs(args []string) []string {
	var flags, nonFlags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// If it's a flag with = (e.g. --format=json), it already has its value
			if strings.Contains(arg, "=") {
				continue
			}
			// If it's a known boolean flag, it doesn't take a value
			if isBoolFlag(arg) {
				continue
			}
			// Check if next arg is value
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			nonFlags = append(nonFlags, arg)
		}
	}
	return append(flags, nonFlags...)
}

func isBoolFlag(flagName string) bool {
	name := strings.TrimLeft(flagName, "-")
	switch name {
	case "json", "fake", "open", "starter":
		return true
	default:
		return false
	}
}

