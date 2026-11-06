package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// WithAuth wraps handler to validate Bearer tokens for /v1/* routes.
// clientToken authorizes the control plane; workerToken authorizes worker
// protocol and artifact transfer used during remote execution.
// /healthz and non-/v1 paths stay public.
func WithAuth(handler http.Handler, clientToken, workerToken string, grants ...AccessGrant) http.Handler {
	clientToken = strings.TrimSpace(clientToken)
	workerToken = strings.TrimSpace(workerToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			handler.ServeHTTP(w, r)
			return
		}
		tok := bearerCredential(r)
		loopback := requestFromLoopback(r)
		for _, grant := range grants {
			if tokenMatches(tok, grant.Token) {
				if grant.authorize(r) {
					handler.ServeHTTP(w, r)
				} else {
					writeError(w, http.StatusForbidden, CodeUnauthorized, "credential does not authorize this resource or operation")
				}
				return
			}
		}
		if clientToken == "" && workerToken == "" && len(grants) == 0 && loopback {
			handler.ServeHTTP(w, r)
			return
		}
		switch {
		case isWorkerProtocolPath(r.URL.Path, r.Method):
			if authorizeWorkerProtocol(tok, clientToken, workerToken, loopback) {
				handler.ServeHTTP(w, r)
				return
			}
		case strings.HasPrefix(r.URL.Path, "/v1/artifacts/"):
			if authorizeArtifacts(tok, clientToken, workerToken, r.Method, r.URL.Path) {
				handler.ServeHTTP(w, r)
				return
			}
		default:
			if tokenMatches(tok, clientToken) {
				handler.ServeHTTP(w, r)
				return
			}
			if workerToken != "" && tokenMatches(tok, workerToken) {
				writeError(w, http.StatusForbidden, CodeUnauthorized, "worker token cannot access coordinator control API")
				return
			}
		}
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or missing authorization token")
	})
}

func bearerCredential(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func tokenMatches(got, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func requestFromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func authorizeWorkerProtocol(tok, clientToken, workerToken string, loopback bool) bool {
	if workerToken != "" && tokenMatches(tok, workerToken) {
		return true
	}
	if loopback && tokenMatches(tok, clientToken) {
		return true
	}
	return false
}

func authorizeArtifacts(tok, clientToken, workerToken, method, path string) bool {
	if tokenMatches(tok, clientToken) {
		return true
	}
	return workerToken != "" && tokenMatches(tok, workerToken)
}

func isWorkerProtocolPath(path, method string) bool {
	if path == "/v1/workers/register" {
		return method == http.MethodPost
	}
	rest := strings.TrimPrefix(path, "/v1/workers/")
	if rest == path {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		return false
	}
	switch parts[1] {
	case "heartbeat":
		return method == http.MethodPost && len(parts) == 2
	case "assignments":
		if len(parts) == 2 {
			return method == http.MethodGet
		}
		if len(parts) == 3 && parts[2] == "log" {
			return method == http.MethodPost
		}
		if len(parts) == 3 && parts[2] == "complete" {
			return method == http.MethodPost
		}
	}
	return false
}
