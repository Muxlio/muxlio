package artifacts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestInterruptedDownloadResumesOrReportsMissingData(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(strconv.FormatBool(broken), func(t *testing.T) {
			payload := "verified-artifact-content"
			digest := digestOf(payload)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/transfer") {
					json.NewEncoder(w).Encode(TransferPlan{Digest: digest, Method: "GET", Mode: "proxy", URL: "/blob"})
					return
				}
				calls++
				if calls == 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					w.Write([]byte(payload[:5])) // The advertised body is interrupted.
					return
				}
				if r.Header.Get("Range") != "bytes=5-" {
					t.Errorf("range=%q", r.Header.Get("Range"))
				}
				if broken {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Range", "bytes 5-24/25")
				w.WriteHeader(206)
				w.Write([]byte(payload[5:]))
			}))
			defer srv.Close()
			var dest strings.Builder
			err := (Client{BaseURL: srv.URL}).Download(t.Context(), digest, int64(len(payload)), &dest)
			if broken {
				var incomplete *IncompleteTransfer
				if !errors.As(err, &incomplete) || incomplete.Digest != digest || incomplete.Received != 5 || incomplete.Expected != int64(len(payload)) || dest.Len() != 0 || calls != 3 {
					t.Fatalf("err=%v bytes=%q calls=%d", err, dest.String(), calls)
				}
			} else if err != nil || dest.String() != payload {
				t.Fatalf("err=%v bytes=%q", err, dest.String())
			}
		})
	}
}

func TestDownloadRejectsCorruptBytesBeforePublishing(t *testing.T) {
	digest := digestOf("good")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			json.NewEncoder(w).Encode(TransferPlan{Digest: digest, Method: "GET", Mode: "proxy", URL: "/blob"})
			return
		}
		w.Write([]byte("evil"))
	}))
	defer srv.Close()
	var dest strings.Builder
	err := (Client{BaseURL: srv.URL}).Download(t.Context(), digest, 4, &dest)
	if !errors.Is(err, ErrIntegrity) || dest.Len() != 0 {
		t.Fatalf("err=%v bytes=%q", err, dest.String())
	}
}

func TestTransferSizeBound(t *testing.T) {
	client := Client{BaseURL: "http://unused.invalid"}
	if _, err := client.Upload(t.Context(), digestOf(""), "", MaxTransferBytes+1, strings.NewReader("")); err == nil {
		t.Fatal("oversized upload accepted")
	}
	if err := client.Download(t.Context(), digestOf(""), MaxTransferBytes+1, &strings.Builder{}); err == nil {
		t.Fatal("oversized download accepted")
	}
}

func TestResumeValidatesRangesAndRestartsWhenIgnored(t *testing.T) {
	for _, contentRange := range []string{"ignored", "bytes 0-5/6", "bytes 2-5/7", "bytes 2-4/6", "invalid"} {
		t.Run(contentRange, func(t *testing.T) {
			payload := "abcdef"
			digest := digestOf(payload)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/transfer") {
					json.NewEncoder(w).Encode(TransferPlan{Digest: digest, Method: "GET", Mode: "proxy", URL: "/blob"})
					return
				}
				calls++
				if calls == 1 {
					w.Header().Set("Content-Length", "6")
					w.Write([]byte(payload[:2]))
					return
				}
				if contentRange == "ignored" {
					w.Write([]byte(payload))
					return
				}
				w.Header().Set("Content-Range", contentRange)
				w.WriteHeader(206)
				w.Write([]byte(payload[2:]))
			}))
			defer srv.Close()
			var dest strings.Builder
			err := (Client{BaseURL: srv.URL}).Download(t.Context(), digest, 6, &dest)
			if contentRange == "ignored" {
				if err != nil || dest.String() != payload {
					t.Fatalf("restart: %v %q", err, dest.String())
				}
			} else if !errors.Is(err, ErrIntegrity) || dest.Len() != 0 {
				t.Fatalf("invalid range published: %v %q", err, dest.String())
			}
		})
	}
}

func TestParseDigestRejectsNonCanonicalHex(t *testing.T) {
	full := digestOf("payload")
	if _, err := parseDigest("sha256:" + strings.ToUpper(full[7:])); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("uppercase hex: %v", err)
	}
}

func TestUploadRejectsNonCanonicalDeclaredDigest(t *testing.T) {
	payload := "payload"
	upper := "sha256:" + strings.ToUpper(digestOf(payload)[7:])
	_, err := (Client{BaseURL: "http://unused.invalid"}).Upload(t.Context(), upper, "", int64(len(payload)), strings.NewReader(payload))
	if !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("expected ErrInvalidDigest, got %v", err)
	}
}

func TestUploadRejectsDeclaredSizeMismatch(t *testing.T) {
	_, err := (Client{BaseURL: "http://unused.invalid"}).Upload(t.Context(), digestOf("abc"), "text/plain", 4, strings.NewReader("abc"))
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("size mismatch: %v", err)
	}
}
