package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// TransferPlan tells a remote worker how to move bytes for one digest.
type TransferPlan struct {
	Digest         string `json:"digest"`
	Method         string `json:"method"`
	Mode           string `json:"mode"` // proxy or direct
	URL            string `json:"url"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	ThresholdBytes int64  `json:"thresholdBytes,omitempty"`
	Mime           string `json:"mime,omitempty"`
}

// Planner selects proxy vs direct transfer for a blob size.
type Planner struct {
	Driver    Driver
	Threshold int64
	S3        *S3
}

// PlannerFromDriver builds a planner from the configured artifact driver.
func PlannerFromDriver(d Driver) Planner {
	p := Planner{Driver: d, Threshold: 1 << 20}
	switch t := d.(type) {
	case Tiered:
		p.Threshold = t.Threshold
		if s3, ok := t.S3.(*S3); ok {
			p.S3 = s3
		}
	case Fallback:
		if s3, ok := t.Primary.(*S3); ok {
			p.S3 = s3
		}
	case *S3:
		p.S3 = t
	}
	return p
}

const transferTTL = 15 * time.Minute

// Plan chooses coordinator proxy for blobs below threshold, otherwise a direct S3 URL when configured.
func (p Planner) Plan(ctx context.Context, digest, method string, size int64, _ any) (TransferPlan, error) {
	if method != http.MethodGet && method != http.MethodPut {
		return TransferPlan{}, fmt.Errorf("muxlio: unsupported transfer method %q", method)
	}
	if _, err := parseDigest(digest); err != nil {
		return TransferPlan{}, err
	}
	plan := TransferPlan{
		Digest:         digest,
		Method:         method,
		ThresholdBytes: p.Threshold,
	}
	if size >= p.Threshold && p.S3 != nil {
		expires := transferTTL
		direct, err := p.S3.PresignedURL(ctx, method, digest, expires)
		if err != nil {
			return TransferPlan{}, err
		}
		plan.Mode = "direct"
		plan.URL = direct
		plan.ExpiresAt = time.Now().UTC().Add(expires).Format(time.RFC3339)
		return plan, nil
	}
	plan.Mode = "proxy"
	plan.URL = "/v1/artifacts/" + digest
	return plan, nil
}

// Client moves artifact bytes using coordinator transfer plans.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Download fetches an artifact and verifies its digest.
func (c Client) Download(ctx context.Context, digest string, size int64, dest io.Writer) error {
	return c.transfer(ctx, digest, size, http.MethodGet, dest, nil)
}

// Upload stores bytes and verifies the declared digest before commit.
func (c Client) Upload(ctx context.Context, digest, mime string, size int64, src io.Reader) (Ref, error) {
	tmp, err := os.CreateTemp("", "muxlio-worker-upload-*")
	if err != nil {
		return Ref{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), &contextReader{ctx: ctx, r: src})
	if err != nil {
		tmp.Close()
		return Ref{}, err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: uploaded digest %q does not match expected %q: %w", got, digest, ErrIntegrity)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return Ref{}, err
	}
	if size != n {
		size = n
	}
	if err := c.transfer(ctx, digest, size, http.MethodPut, nil, tmp); err != nil {
		tmp.Close()
		return Ref{}, err
	}
	// http.Client closes the request body (*os.File) after a successful Do.
	return Ref{Digest: digest, Mime: mime, Size: n}, nil
}

func (c Client) transfer(ctx context.Context, digest string, size int64, method string, dest io.Writer, body io.Reader) error {
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil {
		return err
	}
	planURL := *base
	planURL.Path = strings.TrimRight(planURL.Path, "/") + "/v1/artifacts/" + digest + "/transfer"
	q := planURL.Query()
	q.Set("method", method)
	if size > 0 {
		q.Set("size", strconv.FormatInt(size, 10))
	}
	planURL.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, planURL.String(), nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("coordinator transfer plan HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var plan TransferPlan
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		return err
	}
	target, err := url.Parse(plan.URL)
	if err != nil {
		return err
	}
	if plan.Mode == "proxy" {
		base.Path = strings.TrimRight(base.Path, "/") + target.Path
		target = base
	} else if plan.Mode != "direct" {
		return fmt.Errorf("muxlio: invalid transfer mode %q", plan.Mode)
	}
	var payload io.Reader
	if body != nil {
		payload = body
	}
	op, err := http.NewRequestWithContext(ctx, method, target.String(), payload)
	if err != nil {
		return err
	}
	if plan.Mode == "proxy" {
		op.Header.Set("X-Artifact-SHA256", digest)
		if method == http.MethodPut && body != nil {
			op.Header.Set("Content-Type", "application/octet-stream")
		}
		if c.Token != "" {
			op.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	res, err := c.httpClient().Do(op)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("artifact transfer HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	if method == http.MethodGet {
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(dest, h), res.Body); err != nil {
			return err
		}
		if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
			return fmt.Errorf("muxlio: downloaded artifact %q: %w", digest, ErrIntegrity)
		}
	}
	return nil
}

func parseDigest(digest string) (string, error) {
	if len(digest) != 71 || digest[:7] != "sha256:" {
		return "", fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
	}
	if _, err := hex.DecodeString(digest[7:]); err != nil {
		return "", fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
	}
	return digest, nil
}
