package artifacts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestDownloadResumesAcrossMultiplePartialResponses(t *testing.T) {
	payload := strings.Repeat("x", 25)
	digest := digestOf(payload)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			json.NewEncoder(w).Encode(TransferPlan{Digest: digest, Method: "GET", Mode: "proxy", URL: "/blob"})
			return
		}
		calls++
		switch calls {
		case 1:
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Write([]byte(payload[:5]))
		case 2:
			if r.Header.Get("Range") != "bytes=5-" {
				t.Fatalf("range2=%q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 5-14/25")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(payload[5:15]))
		case 3:
			if r.Header.Get("Range") != "bytes=15-" {
				t.Fatalf("range3=%q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 15-24/25")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(payload[15:]))
		default:
			t.Fatalf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	var dest strings.Builder
	if err := (Client{BaseURL: srv.URL}).Download(t.Context(), digest, int64(len(payload)), &dest); err != nil {
		t.Fatalf("download: %v calls=%d", err, calls)
	}
	if dest.String() != payload {
		t.Fatalf("got len=%d want %d", dest.Len(), len(payload))
	}
}

func TestDownloadResumesAfterShortPartialResponse(t *testing.T) {
	payload := strings.Repeat("y", 25)
	digest := digestOf(payload)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			json.NewEncoder(w).Encode(TransferPlan{Digest: digest, Method: "GET", Mode: "proxy", URL: "/blob"})
			return
		}
		calls++
		switch calls {
		case 1:
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Write([]byte(payload[:5]))
		case 2:
			if r.Header.Get("Range") != "bytes=5-" {
				t.Errorf("range2=%q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 5-14/25")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(payload[5:10]))
		case 3:
			if r.Header.Get("Range") != "bytes=10-" {
				t.Errorf("range3=%q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 10-24/25")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(payload[10:]))
		default:
			t.Fatalf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	var dest strings.Builder
	if err := (Client{BaseURL: srv.URL}).Download(t.Context(), digest, int64(len(payload)), &dest); err != nil {
		t.Fatalf("download: %v calls=%d", err, calls)
	}
	if dest.String() != payload {
		t.Fatalf("got len=%d want %d", dest.Len(), len(payload))
	}
}
