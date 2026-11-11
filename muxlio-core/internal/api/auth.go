package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Caller is the authenticated identity propagated to handlers via request
// context. IsAdmin denotes the coordinator control-plane credential or a
// trusted loopback caller, which is authorized for every operation. A scoped
// AccessGrant sets Permissions to its granted scope; handlers enforce any
// additional permissions required by the resource (e.g. an input request).
type Caller struct {
	ID          string
	IsAdmin     bool
	Permissions []string
}

// HasPermission reports whether the caller holds the given permission. Admin
// callers hold every permission.
func (c Caller) HasPermission(p string) bool {
	if c.IsAdmin {
		return true
	}
	for _, perm := range c.Permissions {
		if perm == p {
			return true
		}
	}
	return false
}

// HasAllPermissions reports whether the caller holds every permission in the
// list. An empty list is satisfied by any caller. Admin callers always pass.
func (c Caller) HasAllPermissions(perms []string) bool {
	for _, p := range perms {
		if !c.HasPermission(p) {
			return false
		}
	}
	return true
}

type callerContextKey struct{}

// CallerFromRequest returns the authenticated Caller, or a zero-value Caller
// (no permissions, not admin) when no caller was attached.
func CallerFromRequest(r *http.Request) Caller {
	if c, ok := r.Context().Value(callerContextKey{}).(Caller); ok {
		return c
	}
	return Caller{}
}

func withCaller(r *http.Request, c Caller) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerContextKey{}, c))
}

// WithReloadingAuth reads the current credential snapshot for every API request.
// Operators can atomically replace credential files to rotate or revoke access
// without restarting the coordinator or changing any assignment fencing fields.
// A failed reload denies access instead of retaining a stale authorization.
func WithReloadingAuth(handler http.Handler, clientToken string, load func() (string, []AccessGrant, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			handler.ServeHTTP(w, r)
			return
		}
		workerToken, grants, err := load()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, CodeUnauthorized, "credential configuration unavailable")
			return
		}
		WithAuth(handler, clientToken, workerToken, grants...).ServeHTTP(w, r)
	})
}

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
		identity := fmt.Sprintf("credential:%x", sha256.Sum256([]byte(tok)))
		loopback := requestFromLoopback(r)
		for _, grant := range grants {
			if tokenMatches(tok, grant.Token) {
				if grant.authorize(r) {
					handler.ServeHTTP(w, withCaller(r, Caller{ID: identity, Permissions: grant.Permissions}))
				} else {
					writeError(w, http.StatusForbidden, CodeUnauthorized, "credential does not authorize this resource or operation")
				}
				return
			}
		}
		if clientToken == "" && workerToken == "" && len(grants) == 0 && loopback {
			handler.ServeHTTP(w, withCaller(r, Caller{ID: "local-operator", IsAdmin: true}))
			return
		}
		switch {
		case isWorkerProtocolPath(r.URL.Path, r.Method):
			if authorizeWorkerProtocol(tok, clientToken, workerToken, loopback) {
				handler.ServeHTTP(w, withCaller(r, Caller{IsAdmin: loopback && tokenMatches(tok, clientToken)}))
				return
			}
		case strings.HasPrefix(r.URL.Path, "/v1/artifacts/"):
			if authorizeArtifacts(tok, clientToken, workerToken, r.Method, r.URL.Path) {
				handler.ServeHTTP(w, withCaller(r, Caller{IsAdmin: tokenMatches(tok, clientToken)}))
				return
			}
		default:
			if tokenMatches(tok, clientToken) {
				handler.ServeHTTP(w, withCaller(r, Caller{ID: identity, IsAdmin: true}))
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
		if len(parts) == 3 && parts[2] == "accounting" {
			return method == http.MethodPost
		}
		if len(parts) == 3 && parts[2] == "complete" {
			return method == http.MethodPost
		}
	}
	return false
}
