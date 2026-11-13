package main

import "strings"

// partitionArgs splits argv into global-option prefix and command invocation.
// Only flags that appear before the subcommand name are treated as globals, so
// "muxlio init --workspace DIR" keeps --workspace for the init FlagSet.
func longFlagName(arg string) string {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		return name[:idx]
	}
	return name
}

func longFlagOptionalValue(arg string) (value string, hasValue bool) {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		return name[idx+1:], true
	}
	return "", false
}

func partitionArgs(args []string) (before, cmdAndRest []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if _, embedded := longFlagOptionalValue(arg); !embedded && globalFlagTakesValue(arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		if i == 0 {
			return nil, args
		}
		return args[:i], args[i:]
	}
	return args, nil
}

func globalFlagTakesValue(flag string) bool {
	switch longFlagName(flag) {
	case "fake-config", "data-dir", "adapters", "workspace", "workspace-id", "addr", "coordinator", "worker-id", "capabilities", "capacity", "state", "server", "token", "token-file", "worker-token", "worker-token-file":
		return true
	default:
		return false
	}
}

// argvIncludesJSON reports whether --json appears anywhere in argv. Unlike
// partitionArgs, this matches flags after the subcommand (muxlio check plan.ts --json).
func argvIncludesJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

// withoutArg drops every occurrence of a boolean flag from argv.
func withoutArg(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == flag {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// absorbFlagValue removes --name VALUE from args when present and returns the
// effective value (flag wins over current).
func absorbFlagValue(name string, args []string, current string) ([]string, string) {
	prefix := "--" + name
	out := make([]string, 0, len(args))
	val := current
	for i := 0; i < len(args); i++ {
		if embedded, ok := longFlagOptionalValue(args[i]); ok && longFlagName(args[i]) == name {
			val = embedded
			continue
		}
		if args[i] == prefix && i+1 < len(args) {
			val = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out, val
}

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
	case "json", "fake", "open", "starter", "allow-command", "current", "dry-run", "list-requests":
		return true
	default:
		return false
	}
}
