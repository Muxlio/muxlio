package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestExpiredArtifactHTTPGone(t *testing.T) {
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := local.Put([]byte("expired"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Expire(b.Digest); err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithArtifacts(state.New(), nil, local)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v1/artifacts/"+b.Digest, nil))
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), CodeArtifactExpired) {
			t.Fatalf("%s %d: %s", method, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/artifacts/sha256:"+strings.Repeat("0", 64), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing %d", response.Code)
	}
}
