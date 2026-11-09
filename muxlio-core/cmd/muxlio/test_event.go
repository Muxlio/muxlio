package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/trigger"
)

// cmdTestEvent sends a real signed delivery through the public webhook route.
// It deliberately needs the trigger secret, not an administrator credential.
func cmdTestEvent(args []string, dataDir string) int {
	fs := flag.NewFlagSet("test-event", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL or profile")
	secretFile := fs.String("secret-file", "", "file containing trigger HMAC secret")
	key := fs.String("idempotency-key", "", "delivery idempotency key")
	header := fs.String("signature-header", trigger.DefaultSignatureHeader, "signature header")
	prefix := fs.String("signature-prefix", "", "signature prefix")
	encoding := fs.String("signature-encoding", trigger.EncodingHex, "hex or base64")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 || *secretFile == "" {
		fmt.Fprintln(os.Stderr, "usage: muxlio test-event --server URL --secret-file FILE [--idempotency-key KEY] trigger-id payload.json (creates a real run)")
		return 2
	}
	serverURL, _, err := resolveServer(*server, dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tr := trigger.Trigger{ID: fs.Arg(0), WorkflowDigest: "test-event", Secret: strings.TrimSpace(string(secret)), SignatureHeader: *header, SignaturePrefix: *prefix, SignatureEncoding: *encoding}
	if err := tr.Normalize(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	f, err := os.Open(fs.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, trigger.MaxPayloadLimitBytes+1))
	if err != nil || len(body) > trigger.MaxPayloadLimitBytes {
		fmt.Fprintln(os.Stderr, "cannot read payload or payload exceeds maximum size")
		return 1
	}
	mac := hmac.New(sha256.New, []byte(tr.Secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	if tr.SignatureEncoding == trigger.EncodingBase64 {
		signature = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(serverURL, "/")+"/webhooks/"+url.PathEscape(tr.ID), bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tr.SignatureHeader, tr.SignaturePrefix+signature)
	if *key != "" {
		req.Header.Set("Idempotency-Key", *key)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
