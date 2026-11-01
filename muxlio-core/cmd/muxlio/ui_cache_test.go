package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
