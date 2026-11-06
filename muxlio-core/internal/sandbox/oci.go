package sandbox

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Docker Desktop supplies a maintained Linux VM on macOS. Its containers share
// the VM kernel; this does not claim a dedicated microVM per agent.
type container struct{ desktop bool }

func (d container) Capabilities(c Config) Capabilities {
	name, process := "oci", "linux-namespaces"
	if d.desktop {
		name, process = "docker-vm", "shared-linux-vm"
	}
	return Capabilities{name, "none", "scratch-and-declared-mounts", process, "explicit-only", workspaceKind(c)}
}
func (d container) Launch(c Config, p Policy) (Launch, error) {
	if d.desktop && runtime.GOOS != "darwin" {
		return Launch{}, fmt.Errorf("Docker VM isolation requires macOS with Docker Desktop")
	}
	if !d.desktop && runtime.GOOS != "linux" {
		return Launch{}, fmt.Errorf("required OCI isolation unavailable: Linux is required")
	}
	if p.Image == "" || strings.HasPrefix(p.Image, "-") {
		return Launch{}, fmt.Errorf("required OCI isolation needs a valid params.isolation.image")
	}
	// -v uses ':' as syntax and silently creates missing sources. Reject
	// ambiguous paths and require actual directories before asking the daemon.
	for _, path := range []string{c.AdapterDir, c.Scratch, c.Workspace} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) || strings.ContainsAny(path, ":,\x00\n") {
			return Launch{}, fmt.Errorf("invalid sandbox mount path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return Launch{}, fmt.Errorf("sandbox mount source is not a directory: %s", path)
		}
	}
	engine, err := exec.LookPath("docker")
	if !d.desktop {
		if podman, e := exec.LookPath("podman"); e == nil {
			engine, err = podman, nil
		}
	}
	if err != nil {
		return Launch{}, fmt.Errorf("required OCI isolation unavailable: podman or docker is required")
	}
	prefix := []string{}
	if filepath.Base(engine) == "docker" {
		// The host CLI may use operator Docker configuration; it is never
		// mounted in the guest. Guest HOME remains private scratch.
		home, err := os.UserHomeDir()
		if err != nil {
			return Launch{}, err
		}
		prefix = append(prefix, "--config", filepath.Join(home, ".docker"))
	}
	if d.desktop {
		prefix = append(prefix, "--context", "desktop-linux")
	}
	name := "muxlio-" + strings.ToLower(rand.Text())
	args := append(append([]string{}, prefix...), "run", "--rm", "--pull=never", "--name", name, "-i", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--tmpfs", "/tmp:rw,nosuid,nodev", "-v", c.AdapterDir+":/muxlio/adapters:ro", "-v", c.Scratch+":"+c.Scratch+":rw", "-w", c.Scratch)
	if filepath.Base(engine) == "podman" {
		args = append(args, "--userns=keep-id")
	} else {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	if c.Workspace != "" && (c.WorkspaceMode == "read-only" || c.WorkspaceMode == "shared-write") {
		mode := "ro"
		if c.WorkspaceMode == "shared-write" {
			mode = "rw"
		}
		args = append(args, "-v", c.Workspace+":"+c.Workspace+":"+mode)
	}
	// The host CLI needs its operator environment (for example rootless Podman's
	// XDG_RUNTIME_DIR). Only explicit guest values cross the boundary; secrets
	// travel in the request stdin. Never use bare --env NAME, which inherits a
	// host value. Override the image entrypoint so every image runs the adapter.
	for _, env := range guestEnvironment(c) {
		if !strings.Contains(env, "=") || strings.ContainsRune(env, '\x00') {
			return Launch{}, fmt.Errorf("sandbox environment requires explicit KEY=value entries")
		}
		args = append(args, "--env", env)
	}
	args = append(args, "--entrypoint", "/muxlio/adapters/"+filepath.Base(c.Script), p.Image)
	args = append(args, c.Arguments...)
	cmd := exec.Command(engine, args...)
	cmd.Env = os.Environ()
	return Launch{Command: cmd, Cleanup: func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := append(append([]string{}, prefix...), "rm", "--force", name)
		cleanup := exec.CommandContext(ctx, engine, args...)
		cleanup.Env = cmd.Env
		return cleanup.Run()
	}}, nil
}
