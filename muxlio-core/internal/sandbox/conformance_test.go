package sandbox_test

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/sandbox"
)

// This payload uses actual reads, writes, process enumeration and sockets.
// Compile a static Linux binary so OCI images need no shell or probe tooling.
const probeSource = `package main
import("encoding/json";"fmt";"net";"os";"path/filepath";"strings";"time")
type Input struct { Host, Credential, Other, Workspace, Scratch, Process, Address, Marker string; ReadOnly bool }
func main(){
 if len(os.Args)!=2 || os.Args[1]!="--conformance-probe" {panic("adapter arguments missing")}
 var p Input; must(json.NewDecoder(os.Stdin).Decode(&p))
 for _, path := range []string{p.Host,p.Credential,p.Other} {
  if _,err:=os.ReadFile(path); err==nil {panic("host file readable: "+path)}
  if err:=os.WriteFile(path,[]byte("escaped"),0600);err==nil {panic("host file writable: "+path)}
 }
 if os.Getenv("MUXLIO_CONFORMANCE_CREDENTIAL")!="" {panic("ambient credential leaked")}
 if os.Getenv("HOME")!=p.Scratch || os.Getenv("TMPDIR")!=p.Scratch {panic("guest environment not private")}
 data,err:=os.ReadFile(filepath.Join(p.Workspace,"file"));must(err)
 if string(data)!="original" {panic("workspace contaminated")}
 err=os.WriteFile(filepath.Join(p.Workspace,"file"),[]byte(p.Marker),0600)
 if p.ReadOnly {if err==nil {panic("read-only workspace writable")}} else {must(err)}
 must(os.WriteFile(filepath.Join(p.Scratch,"proof"),[]byte(p.Marker),0600))
 processes,err:=os.ReadDir("/proc");must(err)
 for _,entry:=range processes {b,_:=os.ReadFile(filepath.Join("/proc",entry.Name(),"cmdline"));if strings.Contains(string(b),p.Process){panic("host process visible")}}
 interfaces,err:=net.Interfaces();must(err)
 for _,i:=range interfaces {if i.Flags&net.FlagUp!=0 && i.Flags&net.FlagLoopback==0 {panic("external network interface available")}}
 conn,err:=net.DialTimeout("tcp",p.Address,500*time.Millisecond)
 if err==nil {conn.Close();panic("host network reachable")}
 fmt.Print("conformant")
}
func must(err error){if err!=nil {panic(err)}}
`

func TestBackendConformance(t *testing.T) {
	backend := os.Getenv("MUXLIO_SANDBOX_CONFORMANCE")
	if backend == "" {
		if runtime.GOOS != "linux" {
			t.Skip("set MUXLIO_SANDBOX_CONFORMANCE=docker-vm with MUXLIO_SANDBOX_IMAGE to test Docker Desktop")
		}
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap unavailable; explicitly select an installed backend to require conformance")
		}
		backend = "bwrap"
	}
	p := sandbox.Policy{Mode: "required", Runtime: backend, Enabled: true, Image: os.Getenv("MUXLIO_SANDBOX_IMAGE")}
	if !sandbox.Supported(backend) {
		t.Fatalf("unsupported conformance backend %q", backend)
	}
	if sandbox.NeedsImage(backend) && p.Image == "" {
		t.Fatal("MUXLIO_SANDBOX_IMAGE must name a locally available, native-architecture Linux image")
	}
	adapters := t.TempDir()
	// macOS /var is a symlink; the worker likewise canonicalizes scratch paths.
	canonical := func(path string) string {
		result, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	adapters = canonical(adapters)
	source := filepath.Join(t.TempDir(), "probe.go")
	if err := os.WriteFile(source, []byte(probeSource), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(adapters, "probe"), source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	host := canonical(t.TempDir())
	for _, name := range []string{"host-file", "credential"} {
		if err := os.WriteFile(filepath.Join(host, name), []byte("host-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MUXLIO_CONFORMANCE_CREDENTIAL", "ambient-secret")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	hostProcess, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	scratches := []string{canonical(t.TempDir()), canonical(t.TempDir())}
	for _, scratch := range scratches {
		if err := os.WriteFile(filepath.Join(scratch, "other-secret"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Run two independent writable clones concurrently, then explicit read-only
	// and writable host mounts. Every probe also attacks the other scratch tree.
	t.Run("concurrent-private-clones", func(t *testing.T) {
		for i := range 2 {
			t.Run(string(rune('a'+i)), func(t *testing.T) {
				t.Parallel()
				workspace := filepath.Join(scratches[i], "workspace")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(workspace, "file"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				runProbe(t, p, adapters, scratches[i], workspace, "snapshot", host, filepath.Join(scratches[1-i], "other-secret"), hostProcess, listener.Addr().String())
			})
		}
	})
	for _, mode := range []string{"read-only", "shared-write"} {
		t.Run(mode, func(t *testing.T) {
			workspace := canonical(t.TempDir())
			if err := os.WriteFile(filepath.Join(workspace, "file"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			runProbe(t, p, adapters, scratches[0], workspace, mode, host, filepath.Join(scratches[1], "other-secret"), hostProcess, listener.Addr().String())
			data, err := os.ReadFile(filepath.Join(workspace, "file"))
			want := "original"
			if mode == "shared-write" {
				want = "shared-write"
			}
			if err != nil || string(data) != want {
				t.Fatalf("host mount effects: %s %v", data, err)
			}
		})
	}
	for _, name := range []string{"host-file", "credential"} {
		data, err := os.ReadFile(filepath.Join(host, name))
		if err != nil || string(data) != "host-secret" {
			t.Fatalf("host boundary breached: %s %v", data, err)
		}
	}
}

func runProbe(t *testing.T, p sandbox.Policy, adapters, scratch, workspace, mode, host, other, process, address string) {
	t.Helper()
	launch, err := sandbox.Start(sandbox.Config{Script: filepath.Join(adapters, "probe"), AdapterDir: adapters, Scratch: scratch, Workspace: workspace, WorkspaceMode: mode, Arguments: []string{"--conformance-probe"}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Cleanup != nil {
		defer launch.Cleanup()
	}
	input, _ := json.Marshal(map[string]any{"Host": filepath.Join(host, "host-file"), "Credential": filepath.Join(host, "credential"), "Other": other, "Workspace": workspace, "Scratch": scratch, "Process": process, "Address": address, "Marker": mode, "ReadOnly": mode == "read-only"})
	launch.Command.Stdin = bytes.NewReader(input)
	var out bytes.Buffer
	launch.Command.Stdout = &out
	launch.Command.Stderr = &out
	if err := launch.Command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- launch.Command.Wait() }()
	select {
	case err := <-done:
		if err != nil || strings.TrimSpace(out.String()) != "conformant" {
			t.Fatalf("boundary conformance failed: %v\n%s", err, out.String())
		}
	case <-time.After(45 * time.Second):
		_ = launch.Command.Process.Kill()
		<-done
		t.Fatal("backend conformance timed out")
	}
	if data, err := os.ReadFile(filepath.Join(scratch, "proof")); err != nil || string(data) != mode {
		t.Fatalf("scratch write not published: %s %v", data, err)
	}
}
