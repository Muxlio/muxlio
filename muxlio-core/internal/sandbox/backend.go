// Package sandbox owns local process isolation, independently of agent adapters.
package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Policy is workflow input. It selects a backend, never an executable or host
// credential directory. Optional VM-backed execution requires explicit opt-in.
type Policy struct {
	Mode    string `json:"mode"`
	Runtime string `json:"runtime,omitempty"`
	Image   string `json:"image,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
}

type Config struct {
	// Workspace snapshots must already be prepared as independent files under
	// Scratch. A backend mounts them; it does not clone the source repository.
	Script, AdapterDir, Scratch, Workspace, WorkspaceMode string
	// Environment is the explicit guest environment, after worker policy
	// filtering. Container engine configuration belongs to the host launcher
	// and must never be forwarded implicitly into the guest.
	Environment []string
	// Arguments are passed directly to the selected adapter, without a shell.
	Arguments []string
}

// Capabilities describe local boundaries, not an agent's remote service or
// tool-level permissions. A private clone is still a writable host mount of
// scratch; it is not evidence that the process itself is isolated.
type Capabilities struct {
	Backend     string `json:"backend"`
	Network     string `json:"network"`     // host or none
	Filesystem  string `json:"filesystem"`  // host or scratch-and-declared-mounts
	Process     string `json:"process"`     // host, linux-namespaces, or shared-linux-vm
	Credentials string `json:"credentials"` // host-accessible or explicit-only
	Workspace   string `json:"workspace"`   // private-clone, read-only-host, writable-host
}

func (c Capabilities) Isolated() bool {
	return c.Network == "none" && c.Filesystem == "scratch-and-declared-mounts" &&
		(c.Process == "linux-namespaces" || c.Process == "shared-linux-vm") && c.Credentials == "explicit-only"
}

// Launch includes backend-owned cleanup: killing a container CLI alone does
// not guarantee that the daemon stopped its container.
type Launch struct {
	Command      *exec.Cmd
	Capabilities Capabilities
	Cleanup      func() error
}

// Backend implementations own runtime flags, platform checks and lifecycle.
// No backend may silently fall back to native execution.
type Backend interface {
	Capabilities(Config) Capabilities
	Launch(Config, Policy) (Launch, error)
}

func workspaceKind(c Config) string {
	switch c.WorkspaceMode {
	case "read-only":
		return "read-only-host"
	case "shared-write":
		return "writable-host"
	default:
		return "private-clone"
	}
}

func Supported(runtime string) bool {
	switch runtime {
	case "bwrap", "oci", "docker-vm", "macos-vm":
		return true
	default:
		return false
	}
}

func NeedsImage(runtime string) bool { return runtime != "bwrap" }
func NeedsOptIn(runtime string) bool { return runtime == "docker-vm" || runtime == "macos-vm" }

func Start(c Config, p Policy) (Launch, error) {
	var backend Backend
	switch p.Mode {
	case "trusted-native":
		if c.WorkspaceMode == "read-only" {
			return Launch{}, fmt.Errorf("read-only workspace requires enforced isolation")
		}
		backend = native{}
	case "required":
		switch p.Runtime {
		case "bwrap":
			backend = bubblewrap{}
		case "oci":
			backend = container{}
		case "docker-vm", "macos-vm":
			if !p.Enabled {
				return Launch{}, fmt.Errorf("VM isolation requires params.isolation.enabled=true")
			}
			backend = container{desktop: true}
		default:
			return Launch{}, fmt.Errorf("no qualifying isolation backend for runtime %q", p.Runtime)
		}
	default:
		return Launch{}, fmt.Errorf("isolation mode must be required or trusted-native")
	}
	caps := backend.Capabilities(c)
	if p.Mode == "required" && !caps.Isolated() {
		return Launch{}, fmt.Errorf("backend does not enforce required isolation")
	}
	launch, err := backend.Launch(c, p)
	if err != nil {
		return Launch{}, err
	}
	launch.Capabilities = caps
	return launch, nil
}

type native struct{}

func (native) Capabilities(c Config) Capabilities {
	workspace := workspaceKind(c)
	if workspace == "read-only-host" {
		workspace = "writable-host" // Native execution cannot enforce a read-only mount.
	}
	return Capabilities{"native-trusted", "host", "host", "host", "host-accessible", workspace}
}
func (native) Launch(c Config, _ Policy) (Launch, error) {
	cmd := exec.Command(c.Script, c.Arguments...)
	cmd.Env = c.Environment
	return Launch{Command: cmd}, nil
}

func guestEnvironment(c Config) []string {
	if c.Environment != nil {
		return c.Environment
	}
	return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + c.Scratch, "TMPDIR=" + c.Scratch, "LANG=C.UTF-8"}
}

// CheckAvailable verifies a configured backend's local launcher. Execution still
// verifies the image, daemon and enforced boundaries for each invocation.
func CheckAvailable(name string) error {
	switch name {
	case "bwrap":
		if runtime.GOOS != "linux" {
			return fmt.Errorf("bwrap requires Linux")
		}
		if _, err := exec.LookPath("bwrap"); err != nil {
			return err
		}
	case "oci":
		if runtime.GOOS != "linux" {
			return fmt.Errorf("OCI isolation requires Linux")
		}
		if _, err := exec.LookPath("podman"); err != nil {
			if _, err := exec.LookPath("docker"); err != nil {
				return err
			}
		}
	case "docker-vm", "macos-vm":
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("Docker VM isolation requires macOS")
		}
		if _, err := exec.LookPath("docker"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown isolation backend %q", name)
	}
	return nil
}
