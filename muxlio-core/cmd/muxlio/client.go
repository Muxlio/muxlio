package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func cmdRemote(command string, args []string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL or profile name")
	tokenFlag := fs.String("token", "", "bearer authentication token")
	tokenFile := fs.String("token-file", "", "file containing bearer authentication token")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	serverURL, profileToken, err := resolveServer(*server, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	token := loadToken(*tokenFlag, *tokenFile)
	if token == "" {
		token = profileToken
	}
	path, method := "/v1/runs", http.MethodGet
	var body io.Reader
	if command == "submit" {
		if fs.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "usage: muxlio submit [--server URL] plan.json input.json")
			return 2
		}
		p, e := os.ReadFile(fs.Arg(0))
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		input, e := os.ReadFile(fs.Arg(1))
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		raw, e := json.Marshal(struct {
			Plan  json.RawMessage `json:"plan"`
			Input json.RawMessage `json:"input"`
		}{p, input})
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		method = http.MethodPost
		body = bytes.NewReader(raw)
	} else if command == "pause" || command == "resume" || command == "cancel" {
		if fs.NArg() != 1 {
			fmt.Fprintf(os.Stderr, "usage: muxlio %s [--server URL] run-id\n", command)
			return 2
		}
		path += "/" + url.PathEscape(fs.Arg(0)) + "/" + command
		method = http.MethodPost
		body = bytes.NewReader([]byte("{}"))
	} else if fs.NArg() > 0 {
		path += "/" + url.PathEscape(fs.Arg(0))
	}
	req, err := http.NewRequest(method, strings.TrimRight(serverURL, "/")+path, body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	setAuthHeader(req, token)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, raw)
		return 1
	}
	fmt.Println(strings.TrimSpace(string(raw)))
	return 0
}
