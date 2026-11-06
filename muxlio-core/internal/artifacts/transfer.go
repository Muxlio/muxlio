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
	Headers        map[string]string `json:"headers,omitempty"`
	Digest         string            `json:"digest"`
	Method         string            `json:"method"`
	Mode           string            `json:"mode"` // proxy or direct
	URL            string            `json:"url"`
	ExpiresAt      string            `json:"expiresAt,omitempty"`
	ThresholdBytes int64             `json:"thresholdBytes,omitempty"`
	Mime           string            `json:"mime,omitempty"`
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

// MaxTransferBytes bounds temporary disk use and each HTTP artifact body.
const MaxTransferBytes int64 = 256 << 20

// IncompleteTransfer identifies missing content without publishing unverified
// bytes. Received is the contiguous prefix retained during this download.
type IncompleteTransfer struct {
	Digest             string
	Received, Expected int64
	Cause              error
}

func (e *IncompleteTransfer) Error() string {
	return fmt.Sprintf("artifact %s incomplete: received %d of %d bytes: %v", e.Digest, e.Received, e.Expected, e.Cause)
}

func (e *IncompleteTransfer) Unwrap() error { return e.Cause }

// Plan chooses coordinator proxy for blobs below threshold, otherwise a direct S3 URL when configured.
func (p Planner) Plan(ctx context.Context, digest, method string, size int64, _ any) (TransferPlan, error) {
	if size < 0 || size > MaxTransferBytes {
		return TransferPlan{}, fmt.Errorf("muxlio: artifact size exceeds transfer limit")
	}
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
		u, err := url.Parse(direct)
		if err != nil {
			return TransferPlan{}, err
		}
		plan.Headers = map[string]string{
			"X-Amz-Date":           u.Query().Get("X-Amz-Date"),
			"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD",
		}
		if method == http.MethodPut {
			plan.Headers["X-Amz-Content-Sha256"] = digest[7:]
		}
		if token := u.Query().Get("X-Amz-Security-Token"); token != "" {
			plan.Headers["X-Amz-Security-Token"] = token
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
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Client) downloadVerified(ctx context.Context, target string, proxy bool, digest string, size int64, headers map[string]string, dest io.Writer) error {
	tmp, err := os.CreateTemp("", "muxlio-worker-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	var received int64
	expected := size
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if proxy && c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if received > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", received))
		}
		res, err := c.httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
			res.Body.Close()
			lastErr = fmt.Errorf("artifact transfer HTTP %d", res.StatusCode)
			if res.StatusCode < 500 {
				break
			}
			continue
		}
		if res.StatusCode == http.StatusPartialContent {
			var start, end, total int64
			n, scanErr := fmt.Sscanf(res.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total)
			if scanErr != nil || n != 3 || start != received || end < start || end != total-1 || total > MaxTransferBytes || (expected > 0 && total != expected) {
				res.Body.Close()
				return fmt.Errorf("muxlio: invalid artifact range: %w", ErrIntegrity)
			}
			expected = total
		} else {
			// Servers without range support can safely restart the bounded download.
			received = 0
			if err := tmp.Truncate(0); err != nil {
				res.Body.Close()
				return err
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				res.Body.Close()
				return err
			}
			if res.ContentLength >= 0 {
				if res.ContentLength > MaxTransferBytes || (expected > 0 && res.ContentLength != expected) {
					res.Body.Close()
					return fmt.Errorf("muxlio: artifact length mismatch: %w", ErrIntegrity)
				}
				expected = res.ContentLength
			}
		}
		n, copyErr := io.Copy(tmp, io.LimitReader(res.Body, MaxTransferBytes-received+1))
		res.Body.Close()
		received += n
		if received > MaxTransferBytes {
			return fmt.Errorf("muxlio: artifact size exceeds transfer limit")
		}
		if copyErr != nil {
			lastErr = copyErr
			continue
		}
		if expected > 0 && received != expected {
			lastErr = io.ErrUnexpectedEOF
			continue
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(h, tmp); err != nil {
			return err
		}
		if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
			return fmt.Errorf("muxlio: downloaded artifact %q: %w", digest, ErrIntegrity)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(dest, tmp)
		return err
	}
	return &IncompleteTransfer{Digest: digest, Received: received, Expected: expected, Cause: lastErr}
}

// Download fetches an artifact and verifies its digest.
func (c Client) Download(ctx context.Context, digest string, size int64, dest io.Writer) error {
	return c.transfer(ctx, digest, size, http.MethodGet, dest, nil)
}

// Upload stores bytes and verifies the declared digest before commit.
func (c Client) Upload(ctx context.Context, digest, mime string, size int64, src io.Reader) (Ref, error) {
	if _, err := parseDigest(digest); err != nil {
		return Ref{}, err
	}
	if size < 0 || size > MaxTransferBytes {
		return Ref{}, fmt.Errorf("muxlio: artifact size exceeds transfer limit")
	}
	tmp, err := os.CreateTemp("", "muxlio-worker-upload-*")
	if err != nil {
		return Ref{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(&contextReader{ctx: ctx, r: src}, MaxTransferBytes+1))
	if err != nil {
		tmp.Close()
		return Ref{}, err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if n > MaxTransferBytes {
		return Ref{}, fmt.Errorf("muxlio: artifact size exceeds transfer limit")
	}
	if got != digest {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: uploaded digest %q does not match expected %q: %w", got, digest, ErrIntegrity)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return Ref{}, err
	}
	if size > 0 && size != n {
		return Ref{}, fmt.Errorf("muxlio: uploaded artifact size mismatch: %w", ErrIntegrity)
	}
	size = n
	if err := c.transfer(ctx, digest, size, http.MethodPut, nil, tmp); err != nil {
		tmp.Close()
		return Ref{}, err
	}
	// http.Client closes the request body (*os.File) after a successful Do.
	return Ref{Digest: digest, Mime: mime, Size: n}, nil
}

func (c Client) transfer(ctx context.Context, digest string, size int64, method string, dest io.Writer, body io.Reader) error {
	if _, err := parseDigest(digest); err != nil {
		return err
	}
	if size < 0 || size > MaxTransferBytes {
		return fmt.Errorf("muxlio: artifact size exceeds transfer limit")
	}
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&plan); err != nil {
		return err
	}
	if plan.Digest != digest || plan.Method != method {
		return fmt.Errorf("muxlio: transfer plan identity mismatch: %w", ErrIntegrity)
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
	if method == http.MethodGet {
		return c.downloadVerified(ctx, target.String(), plan.Mode == "proxy", digest, size, plan.Headers, dest)
	}
	var payload io.Reader
	if body != nil {
		payload = body
	}
	op, err := http.NewRequestWithContext(ctx, method, target.String(), payload)
	if err != nil {
		return err
	}
	op.ContentLength = size
	for key, value := range plan.Headers {
		op.Header.Set(key, value)
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
