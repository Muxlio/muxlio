package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBoundaryDescriptions(t *testing.T) {
	for _, mode := range []string{"snapshot", "read-only", "shared-write"} {
		c := Config{WorkspaceMode: mode}
		want := map[string]string{"snapshot": "private-clone", "read-only": "read-only-host", "shared-write": "writable-host"}[mode]
		for _, b := range []Backend{native{}, bubblewrap{}, container{}, container{desktop: true}} {
			caps := b.Capabilities(c)
			_, isNative := b.(native)
			expected := want
			if isNative && mode == "read-only" {
				expected = "writable-host"
			}
			if caps.Workspace != expected {
				t.Fatalf("workspace boundary: %+v", caps)
			}
			if _, native := b.(native); native {
				if caps.Isolated() || caps.Network != "host" || caps.Filesystem != "host" || caps.Process != "host" || caps.Credentials != "host-accessible" {
					t.Fatalf("native claims isolation: %+v", caps)
				}
			} else if !caps.Isolated() {
				t.Fatalf("missing isolation limits: %+v", caps)
			}
		}
	}
	if (Capabilities{Network: "none", Filesystem: "scratch-and-declared-mounts", Process: "host", Credentials: "explicit-only"}).Isolated() {
		t.Fatal("host process qualified")
	}
}

func TestNativeCannotPromiseReadOnlyWorkspace(t *testing.T) {
	launch, err := Start(Config{WorkspaceMode: "read-only"}, Policy{Mode: "trusted-native"})
	if err == nil || launch.Command != nil {
		t.Fatalf("native claimed a read-only mount: %+v %v", launch, err)
	}
}

func TestUnavailableRequiredBackendNeverReturnsCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, p := range []Policy{
		{Mode: "required"}, {Mode: "invalid"}, {Mode: "required", Runtime: "native-trusted"},
		{Mode: "required", Runtime: "bwrap"}, {Mode: "required", Runtime: "oci", Image: "test"},
		{Mode: "required", Runtime: "docker-vm", Image: "test"},
		{Mode: "required", Runtime: "docker-vm", Image: "test", Enabled: true},
	} {
		launch, err := Start(Config{}, p)
		if err == nil || launch.Command != nil || launch.Capabilities.Isolated() {
			t.Fatalf("failed open: %+v %+v %v", p, launch, err)
		}
	}
}

func TestContainerMountsEnvironmentAndCleanup(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires Linux or macOS")
	}
	bin := t.TempDir()
	// Fake CLI records cleanup only. This test validates command construction;
	// real boundary enforcement is covered by the conformance suite.
	record := filepath.Join(t.TempDir(), "cleanup")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RECORD\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("RECORD", record)
	c := Config{AdapterDir: t.TempDir(), Scratch: t.TempDir(), Workspace: t.TempDir(), WorkspaceMode: "read-only", Script: "/adapter", Arguments: []string{"--muxlio-probe"}}
	p := Policy{Mode: "required", Runtime: "oci", Image: "approved-image"}
	if runtime.GOOS == "darwin" {
		p.Runtime, p.Enabled = "docker-vm", true
	}
	launch, err := Start(c, p)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(launch.Command.Args, " ")
	for _, want := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--env HOME=" + c.Scratch, "--env TMPDIR=" + c.Scratch, "--entrypoint /muxlio/adapters/adapter approved-image --muxlio-probe", c.Workspace + ":" + c.Workspace + ":ro", c.Scratch + ":" + c.Scratch + ":rw"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q: %s", want, args)
		}
	}
	if runtime.GOOS == "darwin" && (!strings.Contains(args, "--context desktop-linux") || launch.Capabilities.Process != "shared-linux-vm") {
		t.Fatal("VM context or boundary missing")
	}
	if err := launch.Cleanup(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil || !strings.Contains(string(data), "rm\n--force\nmuxlio-") {
		t.Fatalf("cleanup missing: %s %v", data, err)
	}
	c.WorkspaceMode = "shared-write"
	launch, err = Start(c, p)
	if err != nil || !strings.Contains(strings.Join(launch.Command.Args, " "), c.Workspace+":"+c.Workspace+":rw") || launch.Capabilities.Workspace != "writable-host" {
		t.Fatalf("shared-write mount: %+v %v", launch, err)
	}
	// Host runtime settings remain available to the CLI and cleanup, while
	// worker filtering applies to the actual guest environment.
	t.Setenv("XDG_RUNTIME_DIR", "/host/runtime")
	t.Setenv("AMBIENT_CREDENTIAL", "host-secret")
	c.Environment = []string{"HOME=" + c.Scratch, "LANG=C.UTF-8"}
	launch, err = Start(c, p)
	if err != nil {
		t.Fatal(err)
	}
	args = strings.Join(launch.Command.Args, " ")
	if !strings.Contains(strings.Join(launch.Command.Env, "\n"), "XDG_RUNTIME_DIR=/host/runtime") || strings.Contains(args, "host-secret") || strings.Contains(args, "--env PATH=") || strings.Contains(args, "--env TMPDIR=") {
		t.Fatalf("host and guest environment mixed: %s", args)
	}
	c.Environment = []string{"AMBIENT_CREDENTIAL"}
	if _, err := Start(c, p); err == nil {
		t.Fatal("implicit host credential inheritance accepted")
	}
	c.Environment = nil
	for _, image := range []string{"", "--privileged"} {
		p.Image = image
		if _, err := Start(c, p); err == nil {
			t.Fatalf("unsafe image %q accepted", image)
		}
	}
}

func TestOCIEngineUsesCompatibleUnprivilegedUserOption(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("OCI isolation is Linux-only")
	}
	for _, tc := range []struct{ engine, want string }{
		{"podman", "--userns=keep-id"}, {"docker", "--user"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, tc.engine), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			c := Config{AdapterDir: t.TempDir(), Scratch: t.TempDir(), Script: "/adapter"}
			launch, err := Start(c, Policy{Mode: "required", Runtime: "oci", Image: "approved-image"})
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Join(launch.Command.Args, " ")
			if !launch.Capabilities.Isolated() || !strings.Contains(args, " -i ") || !strings.Contains(args, c.Scratch+":"+c.Scratch+":rw") || !strings.Contains(args, tc.want) {
				t.Fatalf("invalid unprivileged OCI command: %s", args)
			}
			if tc.engine == "docker" && strings.Contains(args, "--userns=keep-id") {
				t.Fatal("Docker command contains a Podman-only option")
			}
		})
	}
}

func TestNativeAdapterArguments(t *testing.T) {
	launch, err := Start(Config{Script: "/adapter", Arguments: []string{"--muxlio-probe"}}, Policy{Mode: "trusted-native"})
	if err != nil || strings.Join(launch.Command.Args, " ") != "/adapter --muxlio-probe" {
		t.Fatalf("probe arguments: %+v %v", launch, err)
	}
}
