package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// AccessGrant is an operator-issued credential with independent authorization
// for a run, worker identity and content-addressed artifacts. Empty scope denies
// access. The administrator and cluster worker credentials remain trusted roles.
type AccessGrant struct {
	Revoked         bool     `json:"revoked,omitempty"`
	ExpiresAt       string   `json:"expiresAt,omitempty"`
	Token           string   `json:"-"`
	TokenFile       string   `json:"tokenFile"`
	RunID           string   `json:"runId,omitempty"`
	WorkerID        string   `json:"workerId,omitempty"`
	Permissions     []string `json:"permissions"`
	ArtifactDigests []string `json:"artifactDigests,omitempty"`
}

func (g AccessGrant) permits(permission string) bool {
	for _, p := range g.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

func (g AccessGrant) authorize(r *http.Request) bool {
	if g.Revoked {
		return false
	}
	if g.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, g.ExpiresAt)
		if err != nil || !time.Now().Before(expires) {
			return false
		}
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "workers":
		if g.WorkerID == "" || !g.permits("worker") || !isWorkerProtocolPath(r.URL.Path, r.Method) {
			return false
		}
		if parts[1] != "register" {
			return parts[1] == g.WorkerID
		}
		// Registration must bind the body identity too, before calling a backend.
		raw, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var registration WorkerRegistration
		return err == nil && len(raw) <= 4<<20 && json.Unmarshal(raw, &registration) == nil && registration.WorkerID == g.WorkerID
	case "artifacts":
		if len(parts) > 3 || (len(parts) == 3 && parts[2] != "transfer") {
			return false
		}
		found := false
		for _, digest := range g.ArtifactDigests {
			if digest == parts[1] {
				found = true
			}
		}
		if !found {
			return false
		}
		method := r.Method
		if len(parts) == 3 {
			if method != http.MethodGet {
				return false
			}
			method = r.URL.Query().Get("method")
			if method == "" {
				method = http.MethodGet
			}
		}
		return ((method == http.MethodGet || method == http.MethodHead) && g.permits("artifacts:read")) || (method == http.MethodPut && g.permits("artifacts:write"))
	case "runs":
		if g.RunID == "" || parts[1] != g.RunID {
			return false
		}
		if r.Method == http.MethodGet {
			// A run-scoped credential cannot read another history through comparison.
			if len(parts) == 3 && parts[2] == "compare" && r.URL.Query().Get("other") != g.RunID {
				return false
			}
			if len(parts) == 3 && parts[2] == "input-requests" {
				return g.permits("read") || g.permits("inputs")
			}
			return g.permits("read")
		}
		if r.Method != http.MethodPost || len(parts) < 3 {
			return false
		}
		switch parts[2] {
		case "pause", "resume", "cancel", "retry", "start":
			return len(parts) == 3 && g.permits("control")
		case "signals":
			return len(parts) == 4 && g.permits("signals")
		case "input-requests":
			// GET lists outstanding requests; POST submits a response. The
			// request-specific permission check happens in the handler against
			// the request's declared permissions.
			return len(parts) == 4 && g.permits("inputs")
		case "sessions", "steer":
			return g.permits("steer")
		}
	}
	return false
}
