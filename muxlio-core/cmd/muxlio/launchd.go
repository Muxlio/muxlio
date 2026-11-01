package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>io.muxlio.service</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{BINARY_PATH}}</string>
        <string>service</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>WorkingDirectory</key>
    <string>{{HOME_DIR}}</string>
    <key>StandardOutPath</key>
    <string>{{HOME_DIR}}/.muxlio/logs/service.log</string>
    <key>StandardErrorPath</key>
    <string>{{HOME_DIR}}/.muxlio/logs/service.error.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:{{HOME_DIR}}/.muxlio/bin</string>
        <key>MUXLIO_DAEMON</key>
        <string>0</string>
    </dict>
</dict>
</plist>
`

func cmdServerInstall(args []string) int {
	fs := flag.NewFlagSet("server install", flag.ContinueOnError)
	serviceType := fs.String("service", "launchd", "service supervisor to target (launchd)")
	dryRun := fs.Bool("dry-run", false, "print plist without installing or loading")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *serviceType != "launchd" {
		fmt.Fprintf(os.Stderr, "error: unsupported service supervisor %q; only 'launchd' is supported\n", *serviceType)
		return 2
	}

	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: determining binary path: %v\n", err)
		return 1
	}
	execPath, _ = filepath.EvalSymlinks(execPath)
	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "error: $HOME is not set")
		return 1
	}

	plistContent := strings.ReplaceAll(plistTemplate, "{{BINARY_PATH}}", execPath)
	plistContent = strings.ReplaceAll(plistContent, "{{HOME_DIR}}", home)

	if *dryRun {
		fmt.Print(plistContent)
		return 0
	}
	if _, err := ensureAuthToken(filepath.Join(defaultRuntime(), "config.json")); err != nil {
		fmt.Fprintf(os.Stderr, "error: generating coordinator token: %v\n", err)
		return 1
	}

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating LaunchAgents directory: %v\n", err)
		return 1
	}
	logsDir := filepath.Join(home, ".muxlio", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating logs directory: %v\n", err)
		return 1
	}

	plistPath := filepath.Join(launchAgentsDir, "io.muxlio.service.plist")
	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing plist to %s: %v\n", plistPath, err)
		return 1
	}
	fmt.Printf("✓ installed launchd service to %s\n", plistPath)

	// Attempt to reload with launchctl
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: launchctl load failed: %v\n(You can manually run: launchctl load %s)\n", err, plistPath)
	} else {
		fmt.Println("✓ launchd service loaded successfully")
	}
	return 0
}
