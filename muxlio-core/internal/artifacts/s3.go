package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// S3Config configures path-style S3-compatible object storage.
type S3Config struct {
	Endpoint, Bucket, Region, AccessKey, SecretKey, SessionToken string
	Client                                                       *http.Client
}

// S3 stores objects under their immutable SHA-256 digest.
type S3 struct {
	config S3Config
	client *http.Client
}

func NewS3(config S3Config) (*S3, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, fmt.Errorf("muxlio: invalid S3 endpoint")
	}
	if strings.TrimSpace(config.Bucket) == "" || config.AccessKey == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("muxlio: S3 bucket and credentials are required")
	}
	if strings.ContainsAny(config.Bucket, "/\\?#") || config.Bucket == "." || config.Bucket == ".." || strings.TrimSpace(config.Bucket) != config.Bucket {
		return nil, fmt.Errorf("muxlio: invalid S3 bucket")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("muxlio: invalid S3 endpoint")
	}
	if config.Region == "" {
		config.Region = "us-east-1"
	}
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	return &S3{config: config, client: config.Client}, nil
}

func (s *S3) objectURL(digest string) (*url.URL, error) {
	if len(digest) != 71 || digest[:7] != "sha256:" {
		return nil, fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
	}
	for _, c := range digest[7:] {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return nil, fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
		}
	}
	base, _ := url.Parse(s.config.Endpoint)
	base.Path = path.Join(base.Path, s.config.Bucket, digest[7:])
	return base, nil
}

func (s *S3) request(ctx context.Context, method, digest string, body io.Reader, payloadHash string) (*http.Response, error) {
	u, err := s.objectURL(digest)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", stamp)
	if s.config.SessionToken != "" {
		req.Header.Set("x-amz-security-token", s.config.SessionToken)
	}
	canonicalHeaders := "host:" + req.URL.Host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + stamp + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	if s.config.SessionToken != "" {
		canonicalHeaders += "x-amz-security-token:" + s.config.SessionToken + "\n"
		signedHeaders += ";x-amz-security-token"
	}
	canonical := method + "\n" + req.URL.EscapedPath() + "\n\n" + canonicalHeaders + signedHeaders + "\n" + payloadHash
	ch := sha256.Sum256([]byte(canonical))
	scope := date + "/" + s.config.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	dateKey := hmacSHA([]byte("AWS4"+s.config.SecretKey), date)
	regionKey := hmacSHA(dateKey, s.config.Region)
	serviceKey := hmacSHA(regionKey, "s3")
	signingKey := hmacSHA(serviceKey, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.config.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+sig)
	return containedHTTPClient(s.client).Do(req)
}

func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = io.WriteString(h, value)
	return h.Sum(nil)
}

// PresignedURL returns a time-limited SigV4 URL for GET or PUT of an object keyed by digest.
func (s *S3) PresignedURL(ctx context.Context, method, digest string, expires time.Duration) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if method != http.MethodGet && method != http.MethodPut {
		return "", fmt.Errorf("muxlio: unsupported presigned method %q", method)
	}
	u, err := s.objectURL(digest)
	if err != nil {
		return "", err
	}
	if expires <= 0 {
		expires = transferTTL
	}
	if expires > 7*24*time.Hour {
		expires = 7 * 24 * time.Hour
	}
	now := time.Now().UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	payloadHash := "UNSIGNED-PAYLOAD"
	if method == http.MethodPut {
		payloadHash = digest[7:]
	}
	scope := date + "/" + s.config.Region + "/s3/aws4_request"
	credential := s.config.AccessKey + "/" + scope
	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", credential)
	query.Set("X-Amz-Date", stamp)
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	signedHeaders := "host"
	if s.config.SessionToken != "" {
		query.Set("X-Amz-Security-Token", s.config.SessionToken)
		signedHeaders = "host;x-amz-content-sha256;x-amz-date;x-amz-security-token"
	} else {
		signedHeaders = "host;x-amz-content-sha256;x-amz-date"
	}
	query.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQuery := strings.ReplaceAll(query.Encode(), "+", "%20")
	canonicalHeaders := "host:" + u.Host + "\n"
	if method == http.MethodPut || method == http.MethodGet {
		canonicalHeaders += "x-amz-content-sha256:" + payloadHash + "\n"
		canonicalHeaders += "x-amz-date:" + stamp + "\n"
		if s.config.SessionToken != "" {
			canonicalHeaders += "x-amz-security-token:" + s.config.SessionToken + "\n"
		}
	}
	canonical := method + "\n" + u.EscapedPath() + "\n" + canonicalQuery + "\n" + canonicalHeaders + signedHeaders + "\n" + payloadHash
	ch := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	dateKey := hmacSHA([]byte("AWS4"+s.config.SecretKey), date)
	regionKey := hmacSHA(dateKey, s.config.Region)
	serviceKey := hmacSHA(regionKey, "s3")
	signingKey := hmacSHA(serviceKey, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(signingKey, stringToSign))
	query.Set("X-Amz-Signature", sig)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// PutStream stages the stream to determine its content digest before issuing a signed S3 PUT.
func (s *S3) PutStream(ctx context.Context, src io.Reader, mime, expected string) (Ref, error) {
	if expected != "" {
		if _, err := s.objectURL(expected); err != nil {
			return Ref{}, err
		}
	}
	tmp, err := os.CreateTemp("", "muxlio-s3-upload-*")
	if err != nil {
		return Ref{}, fmt.Errorf("muxlio: stage S3 artifact: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), &contextReader{ctx: ctx, r: src})
	if err != nil {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: read S3 artifact upload: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if expected != "" && expected != digest {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: %w", ErrIntegrity)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return Ref{}, err
	}
	// Keep ownership of the staging file; the transport closes only the wrapper.
	resp, err := s.request(ctx, http.MethodPut, digest, io.NopCloser(tmp), digest[7:])
	closeErr := tmp.Close()
	if err != nil {
		return Ref{}, fmt.Errorf("muxlio: S3 upload failed")
	}
	if closeErr != nil {
		resp.Body.Close()
		return Ref{}, closeErr
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Ref{}, fmt.Errorf("muxlio: S3 upload returned HTTP %d", resp.StatusCode)
	}
	return Ref{Digest: digest, Mime: mime, Size: n}, nil
}

// GetStream verifies an object into a local snapshot before exposing any bytes.
func (s *S3) GetStream(ctx context.Context, digest string) (io.ReadCloser, Ref, error) {
	if _, err := s.objectURL(digest); err != nil {
		return nil, Ref{}, err
	}
	resp, err := s.request(ctx, http.MethodGet, digest, nil, hex.EncodeToString(sha256.New().Sum(nil)))
	if err != nil {
		return nil, Ref{}, fmt.Errorf("muxlio: S3 download failed")
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, Ref{}, fmt.Errorf("muxlio: artifact %q: %w", digest, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, Ref{}, fmt.Errorf("muxlio: S3 download returned HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "muxlio-s3-download-*")
	if err != nil {
		resp.Body.Close()
		return nil, Ref{}, err
	}
	name := tmp.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), &contextReader{ctx: ctx, r: resp.Body})
	_ = resp.Body.Close()
	if err != nil {
		tmp.Close()
		os.Remove(name)
		return nil, Ref{}, err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		tmp.Close()
		os.Remove(name)
		return nil, Ref{}, ErrIntegrity
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(name)
		return nil, Ref{}, err
	}
	return &snapshotReadCloser{contextReadCloser: contextReadCloser{ctx: ctx, ReadCloser: tmp}, path: name}, Ref{Digest: digest, Size: n}, nil
}

func (s *S3) Has(digest string) bool {
	r, _, err := s.GetStream(context.Background(), digest)
	if err != nil {
		return false
	}
	_ = r.Close()
	return true
}

// Fallback preserves local availability when optional object storage is unavailable.
type Fallback struct{ Primary, Local Driver }

func (f Fallback) PutStream(ctx context.Context, r io.Reader, mime, digest string) (Ref, error) {
	tmp, err := os.CreateTemp("", "muxlio-artifact-fallback-*")
	if err != nil {
		return Ref{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, &contextReader{ctx: ctx, r: r}); err != nil {
		return Ref{}, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return Ref{}, err
	}
	ref, primaryErr := f.Primary.PutStream(ctx, tmp, mime, digest)
	if primaryErr == nil {
		return ref, nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return Ref{}, err
	}
	return f.Local.PutStream(ctx, tmp, mime, digest)
}
func (f Fallback) GetStream(ctx context.Context, digest string) (io.ReadCloser, Ref, error) {
	r, ref, err := f.Primary.GetStream(ctx, digest)
	if err == nil {
		return r, ref, nil
	}
	return f.Local.GetStream(ctx, digest)
}
func (f Fallback) Has(digest string) bool { return f.Primary.Has(digest) || f.Local.Has(digest) }

// Tiered keeps small artifacts on disk and sends larger ones directly to S3.
type Tiered struct {
	S3, Local Driver
	Threshold int64
}

func (t Tiered) PutStream(ctx context.Context, src io.Reader, mime, expected string) (Ref, error) {
	tmp, err := os.CreateTemp("", "muxlio-artifact-tier-*")
	if err != nil {
		return Ref{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	n, err := io.Copy(tmp, &contextReader{ctx: ctx, r: src})
	if err != nil {
		return Ref{}, err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return Ref{}, err
	}
	if n < t.Threshold {
		return t.Local.PutStream(ctx, tmp, mime, expected)
	}
	ref, err := t.S3.PutStream(ctx, tmp, mime, expected)
	if err == nil {
		return ref, nil
	}
	if _, seekErr := tmp.Seek(0, io.SeekStart); seekErr != nil {
		return Ref{}, seekErr
	}
	return t.Local.PutStream(ctx, tmp, mime, expected)
}
func (t Tiered) GetStream(ctx context.Context, digest string) (io.ReadCloser, Ref, error) {
	r, ref, err := t.S3.GetStream(ctx, digest)
	if err == nil {
		return r, ref, nil
	}
	return t.Local.GetStream(ctx, digest)
}
func (t Tiered) Has(digest string) bool { return t.S3.Has(digest) || t.Local.Has(digest) }

// FromEnvironment selects local storage by default and enables S3 for blobs at
// or above MUXLIO_S3_THRESHOLD_BYTES when MUXLIO_S3_ENDPOINT is configured.
func FromEnvironment(root string) (Driver, error) {
	local, err := NewLocal(root)
	if err != nil {
		return nil, err
	}
	endpoint := os.Getenv("MUXLIO_S3_ENDPOINT")
	if endpoint == "" {
		return local, nil
	}
	threshold := int64(5 << 20)
	if value := os.Getenv("MUXLIO_S3_THRESHOLD_BYTES"); value != "" {
		threshold, err = strconv.ParseInt(value, 10, 64)
		if err != nil || threshold < 0 {
			return nil, fmt.Errorf("muxlio: invalid MUXLIO_S3_THRESHOLD_BYTES")
		}
	}
	remote, err := NewS3(S3Config{Endpoint: endpoint, Bucket: os.Getenv("MUXLIO_S3_BUCKET"), Region: os.Getenv("MUXLIO_S3_REGION"), AccessKey: os.Getenv("MUXLIO_S3_ACCESS_KEY"), SecretKey: os.Getenv("MUXLIO_S3_SECRET_KEY"), SessionToken: os.Getenv("MUXLIO_S3_SESSION_TOKEN")})
	if err != nil {
		return nil, err
	}
	return Tiered{S3: remote, Local: local, Threshold: threshold}, nil
}
