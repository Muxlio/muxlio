package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestServiceUIFreshAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "js"), 0700); err != nil {
		t.Fatal(err)
	}
	assets := map[string]string{"/": "index.html", "/app.css": "app.css", "/js/workbench.js": "js/workbench.js"}
	for _, name := range assets {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("current asset"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := serviceUI(http.NotFoundHandler(), dir, "")
	for path := range assets {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s: status=%d cache=%q", method, path, response.Code, response.Header().Get("Cache-Control"))
			}
		}
	}
}

// Exercise the production handler with the actual page so new script imports
// cannot pass UI tests while remaining unavailable in the installed service.
func TestServiceUIServesPageScripts(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "muxlio-ui")
	page, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	scripts := regexp.MustCompile(`<script\b[^>]*\bsrc=["']([^"']+)["']`).FindAllSubmatch(page, -1)
	if len(scripts) == 0 {
		t.Fatal("page has no scripts to check")
	}
	handler := serviceUI(http.NotFoundHandler(), dir, "")
	for _, script := range scripts {
		path := string(script[1])
		t.Run(path, func(t *testing.T) {
			u, err := url.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(u.Path, "/")))
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("script referenced by index.html is unavailable: HTTP %d", response.Code)
			}
			if !bytes.Equal(response.Body.Bytes(), want) {
				t.Fatal("script response does not match the UI asset")
			}
			if !strings.Contains(response.Header().Get("Content-Type"), "javascript") {
				t.Fatalf("script has incorrect content type: %s", response.Header().Get("Content-Type"))
			}
		})
	}
}
