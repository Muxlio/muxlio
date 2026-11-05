package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// originalArgs preserves the process command line at startup so that daemon
// re-exec can replay the same invocation (including test-harness flags).
var originalArgs = append([]string(nil), os.Args...)

// serviceConfig authorizes native execution within one explicit workspace.
type serviceConfig struct {
	Admission state.AdmissionConfig `json:"admission,omitempty"`
	// configPath identifies the selected credential file independently of RuntimeDir.
	configPath string

	AgentHome             string                            `json:"agentHome"`
	RuntimeDir            string                            `json:"runtimeDir"`
	Addr                  string                            `json:"addr"`
	Adapters              string                            `json:"adapters"`
	UI                    string                            `json:"ui"`
	Workspace             string                            `json:"workspace"`
	Capabilities          []string                          `json:"capabilities"`
	Executors             map[string]workers.ExecutorConfig `json:"executors,omitempty"`
	WorkerEnv             map[string]string                 `json:"workerEnv,omitempty"`
	AttemptTimeoutSeconds int                               `json:"attemptTimeoutSeconds"`
	// Token is an optional bearer token that the coordinator will require on all
	// API requests (except /healthz). Can also be set via MUXLIO_TOKEN env.
	Token     string            `json:"token,omitempty"`
	TokenFile string            `json:"tokenFile,omitempty"`
	TLSCert   string            `json:"tlsCert,omitempty"`
	TLSKey    string            `json:"tlsKey,omitempty"`
	MCP       *serviceMCPConfig `json:"mcp,omitempty"`
}

// A configured MCP listener is owned by the service, alongside the coordinator.
type serviceMCPConfig struct {
	Listen       string   `json:"listen"`
	TokenFile    string   `json:"tokenFile,omitempty"`
	TLSCert      string   `json:"tlsCert,omitempty"`
	TLSKey       string   `json:"tlsKey,omitempty"`
	AllowOrigins []string `json:"allowOrigins,omitempty"`
}

func defaultRuntime() string {
	if value := os.Getenv("MUXLIO_HOME"); value != "" {
		return value
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".muxlio")
}

func loadServiceConfig(path string) (serviceConfig, error) {
	root := defaultRuntime()
	exe, err := os.Executable()
	if err != nil {
		return serviceConfig{}, err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	prefix := filepath.Dir(filepath.Dir(exe))
	c := serviceConfig{RuntimeDir: root, Addr: "127.0.0.1:7600", Adapters: filepath.Join(prefix, "libexec", "muxlio", "adapters"), UI: filepath.Join(prefix, "share", "muxlio", "ui")}
	if path == "" {
		path = os.Getenv("MUXLIO_CONFIG")
	}
	explicit := path != ""
	if path == "" {
		path = filepath.Join(root, "config.json")
	}
	raw, err := os.ReadFile(path)
	configDir := filepath.Dir(path)
	if err == nil {
		if err = json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		if c.TLSCert != "" && !filepath.IsAbs(c.TLSCert) {
			c.TLSCert = filepath.Join(configDir, c.TLSCert)
		}
		if c.TLSKey != "" && !filepath.IsAbs(c.TLSKey) {
			c.TLSKey = filepath.Join(configDir, c.TLSKey)
		}
		if c.MCP != nil {
			if strings.TrimSpace(c.MCP.Listen) == "" {
				return c, fmt.Errorf("mcp.listen is required when mcp is configured")
			}
			for _, field := range []*string{&c.MCP.TokenFile, &c.MCP.TLSCert, &c.MCP.TLSKey} {
				if *field != "" && !filepath.IsAbs(*field) {
					*field = filepath.Join(configDir, *field)
				}
			}
		}
	} else if explicit || !os.IsNotExist(err) {
		return c, err
	}
	c.configPath = path
	for key, dest := range map[string]*string{"MUXLIO_HOME": &c.RuntimeDir, "MUXLIO_ADDR": &c.Addr, "MUXLIO_ADAPTERS": &c.Adapters, "MUXLIO_UI": &c.UI, "MUXLIO_WORKSPACE": &c.Workspace, "MUXLIO_TLS_CERT": &c.TLSCert, "MUXLIO_TLS_KEY": &c.TLSKey} {
		if v := os.Getenv(key); v != "" {
			*dest = v
		}
	}
	if v := os.Getenv("MUXLIO_CAPABILITIES"); v != "" {
		c.Capabilities = strings.Split(v, ",")
	}
	if err := workers.ValidateWorkerEnv(c.WorkerEnv); err != nil {
		return c, fmt.Errorf("workerEnv: %w", err)
	}
	if err := workers.ValidateExecutorConfigs(c.Executors); err != nil {
		return c, err
	}
	return c, nil
}

func cmdService(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: muxlio service <start|stop|restart> [--config path]")
		return 2
	}
	switch args[0] {
	case "start":
		return cmdServiceStart(args[1:])
	case "stop":
		return cmdServiceStop(args[1:])
	case "restart":
		return cmdServiceRestart(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: muxlio service <start|stop|restart> [--config path]")
		return 2
	}
}

func cmdServiceStart(args []string) int {
	// Re-launched as the daemon child (MUXLIO_DAEMON=1) or explicitly in the
	// foreground under a supervisor like launchd (MUXLIO_DAEMON=0): run directly.
	if os.Getenv("MUXLIO_DAEMON") != "" {
		return cmdServiceStartRun(args)
	}
	// Interactive terminal: fork into the background.
	fs := flag.NewFlagSet("service start", flag.ContinueOnError)
	path := fs.String("config", "", "JSON configuration path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := loadServiceConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := validateListenConfig(c.Addr, c.TLSCert, c.TLSKey); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	held, err := serviceLockHeld(c.RuntimeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if held {
		fmt.Fprintln(os.Stderr, "service already running for runtime directory")
		return 1
	}
	if err := os.MkdirAll(filepath.Join(c.RuntimeDir, "logs"), 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logPath := filepath.Join(c.RuntimeDir, "logs", "service.log")
	var logOffset int64
	if st, statErr := os.Stat(logPath); statErr == nil {
		logOffset = st.Size()
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error opening log file:", err)
		return 1
	}
	defer logFile.Close()
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cmd := exec.Command(exe, originalArgs[1:]...)
	cmd.Env = append(os.Environ(), "MUXLIO_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "error starting daemon:", err)
		return 1
	}
	// Wait briefly to detect immediate startup failures (e.g. lock already held,
	// invalid config). If the child is still alive after this window, the
	// service started successfully and the parent can exit.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		snippet := tailLogSince(logPath, logOffset)
		if snippet == "" {
			snippet = "(no new log output)"
		}
		fmt.Fprintf(os.Stderr, "service failed to start:\n%s\n", snippet)
		return 1
	case <-time.After(2 * time.Second):
		fmt.Printf("muxlio service started (pid %d, logs: %s)\n", cmd.Process.Pid, logPath)
		return 0
	}
}

func tailLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	const maxLines = 20
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func tailLogSince(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return ""
	}
	const maxLines = 20
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func serviceLockHeld(runtimeDir string) (bool, error) {
	if _, err := os.Stat(runtimeDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		return false, nil
	}
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return true, nil
	}
	return false, err
}

func cmdServiceStartRun(args []string) int {
	fs := flag.NewFlagSet("service start", flag.ContinueOnError)
	path := fs.String("config", "", "JSON configuration path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := loadServiceConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if c.Token == "" {
		if c.TokenFile != "" {
			if raw, readErr := os.ReadFile(c.TokenFile); readErr == nil {
				c.Token = strings.TrimSpace(string(raw))
			}
		}
	}
	if c.Token == "" {
		c.Token, err = ensureAuthToken(c.configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error configuring coordinator token:", err)
			return 1
		}
	}
	if !filepath.IsAbs(c.RuntimeDir) || !filepath.IsAbs(c.Workspace) {
		fmt.Fprintln(os.Stderr, "runtimeDir and workspace must be absolute paths; configure an authorized workspace")
		return 2
	}
	info, err := os.Stat(c.Workspace)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "workspace must be an existing directory")
		return 2
	}
	if len(c.Capabilities) == 0 {
		fmt.Fprintln(os.Stderr, "configure explicit local worker capabilities")
		return 2
	}
	if c.AttemptTimeoutSeconds < 0 || c.AttemptTimeoutSeconds > 86400 {
		fmt.Fprintln(os.Stderr, "attemptTimeoutSeconds must be between 0 (default) and 86400")
		return 2
	}
	if c.AgentHome != "" {
		info, err := os.Stat(c.AgentHome)
		if !filepath.IsAbs(c.AgentHome) || err != nil || !info.IsDir() {
			fmt.Fprintln(os.Stderr, "agentHome must be an existing absolute directory")
			return 2
		}
	}
	for _, dir := range []string{c.RuntimeDir, filepath.Join(c.RuntimeDir, "data"), filepath.Join(c.RuntimeDir, "logs"), filepath.Join(c.RuntimeDir, "work"), filepath.Join(c.RuntimeDir, "artifacts")} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err = os.Chmod(c.RuntimeDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := os.OpenFile(filepath.Join(c.RuntimeDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "service already running for runtime directory")
		return 1
	}
	pidPath := filepath.Join(c.RuntimeDir, "service.pid")
	if err = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(pidPath)
	return serve(c.RuntimeDir, c.Addr, false, &c)
}

func cmdServiceStop(args []string) int {
	fs := flag.NewFlagSet("service stop", flag.ContinueOnError)
	path := fs.String("config", "", "JSON configuration path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := loadServiceConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return stopRunningService(c, true)
}

func cmdServiceRestart(args []string) int {
	fs := flag.NewFlagSet("service restart", flag.ContinueOnError)
	path := fs.String("config", "", "JSON configuration path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := loadServiceConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if code := stopRunningService(c, false); code != 0 {
		return code
	}
	startArgs := []string(nil)
	if *path != "" {
		startArgs = []string{"--config", *path}
	}
	return cmdServiceStart(startArgs)
}

// stopRunningService stops the coordinator for c.RuntimeDir. When requireRunning
// is false, a missing or stale PID is treated as success (for restart).
func stopRunningService(c serviceConfig, requireRunning bool) int {
	pidPath := filepath.Join(c.RuntimeDir, "service.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			if requireRunning {
				fmt.Fprintln(os.Stderr, "no service running for runtime directory")
				return 1
			}
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID in %s: %s\n", pidPath, strings.TrimSpace(string(raw)))
		return 1
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error locating process:", err)
		return 1
	}
	// A PID file is informational and may be stale or reused. Probe the runtime
	// lock first; only signal a PID while another process holds the lock, and
	// only consider stop complete after that lock becomes available.
	lock, err := os.OpenFile(filepath.Join(c.RuntimeDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = os.Remove(pidPath)
		if requireRunning {
			fmt.Fprintln(os.Stderr, "service process not found (cleaned up stale PID file)")
			return 1
		}
		return 0
	} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
		fmt.Fprintln(os.Stderr, "error checking service lock:", err)
		return 1
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// The lock is held but the recorded PID is not signalable — the process
		// likely crashed without releasing the lock. Remove the stale PID file
		// and wait for the OS to reclaim the lock (typically immediate after a
		// crash), then report success so the caller can start a new service.
		_ = os.Remove(pidPath)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if lockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr == nil {
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				return 0
			}
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprintln(os.Stderr, "service lock is held but recorded process is not found; lock may still be held by an unrelated process")
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "error sending SIGTERM:", err)
		return 1
	}
	// Wait for the service to release its lock. A terminated daemon can remain
	// as a zombie, for which signal 0 continues to succeed after shutdown.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			_ = os.Remove(pidPath)
			return 0
		} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			fmt.Fprintln(os.Stderr, "error checking service lock:", err)
			return 1
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "service did not exit within 15 seconds")
	return 1
}
