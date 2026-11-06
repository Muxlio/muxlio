package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type bubblewrap struct{}

func (bubblewrap) Capabilities(c Config) Capabilities {
	return Capabilities{"bwrap", "none", "scratch-and-declared-mounts", "linux-namespaces", "explicit-only", workspaceKind(c)}
}
func (bubblewrap) Launch(c Config, _ Policy) (Launch, error) {
	if runtime.GOOS != "linux" {
		return Launch{}, fmt.Errorf("required bwrap isolation unavailable: Linux is required")
	}
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		return Launch{}, fmt.Errorf("required bwrap isolation unavailable: %w", err)
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL"}
	// Bind only system executables/libraries. Never bind the host root, home,
	// /run (credential sockets), or /etc (host configuration and secrets).
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	args = append(args, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--ro-bind", c.AdapterDir, "/muxlio/adapters", "--bind", c.Scratch, c.Scratch)
	if c.Workspace != "" && (c.WorkspaceMode == "read-only" || c.WorkspaceMode == "shared-write") {
		flag := "--ro-bind"
		if c.WorkspaceMode == "shared-write" {
			flag = "--bind"
		}
		args = append(args, flag, c.Workspace, c.Workspace)
	}
	args = append(args, "--chdir", c.Scratch, "/muxlio/adapters/"+filepath.Base(c.Script))
	args = append(args, c.Arguments...)
	cmd := exec.Command(binary, args...)
	cmd.Env = guestEnvironment(c)
	return Launch{Command: cmd}, nil
}
