package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type workspaceCatalogRunner struct {
	Runner
	*Coordinator
}

func requestWorkspaceCatalog(handler http.Handler, method, query, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/v1/workspaces"+query, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func readWorkspaceCatalog(t *testing.T, handler http.Handler, query string) WorkspaceCatalogPage {
	t.Helper()
	response := requestWorkspaceCatalog(handler, http.MethodGet, query, "admin")
	var page WorkspaceCatalogPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
		t.Fatalf("catalog: %d %s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" || page.ObservedAt.IsZero() || page.Workspaces == nil {
		t.Fatalf("missing observation metadata: %+v", page)
	}
	if strings.Contains(response.Body.String(), "/private/") || strings.Contains(response.Body.String(), "private-label") {
		t.Fatalf("catalog leaked private data: %s", response.Body)
	}
	return page
}

func TestWorkspaceCatalogAuthorization(t *testing.T) {
	store := state.New()
	coordinator := NewCoordinator(store)
	coordinator.Workspace = "/private/legacy"
	router := NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator})
	handler := WithAuth(router, "admin", "worker",
		AccessGrant{Token: "catalog", Permissions: []string{"workspaces:read"}},
		AccessGrant{Token: "revoked", Revoked: true, Permissions: []string{"workspaces:read"}},
		AccessGrant{Token: "expired", ExpiresAt: "2000-01-01T00:00:00Z", Permissions: []string{"workspaces:read"}},
		AccessGrant{Token: "reader", RunID: "run", Permissions: []string{"read"}},
		AccessGrant{Token: "mixed-run", RunID: "run", Permissions: []string{"read", "workspaces:read"}},
		AccessGrant{Token: "submitter", Permissions: []string{"submit"}},
		AccessGrant{Token: "worker", Permissions: []string{"workspaces:read"}},
		AccessGrant{Token: "scoped-worker", WorkerID: "node", Permissions: []string{"worker"}},
		AccessGrant{Token: "mixed-worker", WorkerID: "node", Permissions: []string{"worker", "workspaces:read"}},
	)
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"admin", 200}, {"catalog", 200}, {"", 401}, {"unknown", 401},
		{"reader", 403}, {"submitter", 403}, {"worker", 403}, {"scoped-worker", 403},
		{"mixed-worker", 403}, {"mixed-run", 403}, {"revoked", 403}, {"expired", 403},
	} {
		t.Run(tc.token, func(t *testing.T) {
			got := requestWorkspaceCatalog(handler, http.MethodGet, "", tc.token)
			if got.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", got.Code, tc.status, got.Body)
			}
		})
	}
	if got := requestWorkspaceCatalog(router, http.MethodGet, "", "admin"); got.Code != http.StatusForbidden {
		t.Fatalf("unwrapped router allowed unauthenticated access: %d", got.Code)
	}
	if got := requestWorkspaceCatalog(handler, http.MethodPost, "", "admin"); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST returned %d", got.Code)
	}
	if got := requestWorkspaceCatalog(handler, http.MethodGet, "/other", "catalog"); got.Code != http.StatusForbidden {
		t.Fatalf("catalog grant authorized another resource: %d", got.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/workspaces", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	WithAuth(router, "", "").ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("accepted implicit loopback operator credential: %d", response.Code)
	}
}

func TestWorkspaceCatalogEmptyAndLegacy(t *testing.T) {
	store := state.New()
	unavailable := WithAuth(NewRouter(store, nil), "admin", "")
	if got := requestWorkspaceCatalog(unavailable, http.MethodGet, "", "admin"); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("catalog without a source returned %d", got.Code)
	}
	coordinator := NewCoordinator(store)
	handler := WithAuth(NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator}), "admin", "")
	if page := readWorkspaceCatalog(t, handler, "?limit=201"); len(page.Workspaces) != 0 || page.Limit != 50 || page.HasMore {
		t.Fatalf("empty catalog: %+v", page)
	}
	coordinator.Workspace = "/private/legacy"
	page := readWorkspaceCatalog(t, handler, "")
	if len(page.Workspaces) != 1 || page.Workspaces[0].WorkspaceID != "default" || page.Workspaces[0].WorkerID != "local" ||
		page.Workspaces[0].Status != "configured" || page.Workspaces[0].OwnerConnected || page.Workspaces[0].DigestConfigured {
		t.Fatalf("legacy catalog: %+v", page)
	}
}

func TestWorkspaceCatalogObservationAndRecovery(t *testing.T) {
	db := filepath.Join(t.TempDir(), "coordinator.db")
	store, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	digest := "sha256:" + strings.Repeat("a", 64)
	registry := workers.WorkspaceRegistry{
		"project":  {WorkerID: "owner", Path: "/private/project", Digest: digest, Aliases: []string{"second", "first"}},
		"disabled": {WorkerID: "disabled", Path: "/private/disabled"},
		"missing":  {WorkerID: "missing", Path: "/private/missing"},
		"stale":    {WorkerID: "stale", Path: "/private/stale"},
		"offline":  {WorkerID: "offline", Path: "/private/offline"},
		"draining": {WorkerID: "draining", Path: "/private/draining"},
	}
	coordinator := NewCoordinator(store)
	coordinator.Workspace, coordinator.Workspaces = "/private/ignored-legacy", registry
	handler := WithAuth(NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator}), "admin", "")

	if page := readWorkspaceCatalog(t, handler, ""); len(page.Workspaces) != len(registry) || page.Workspaces[3].Status != "owner_offline" {
		t.Fatalf("catalog without registrations: %+v", page)
	}
	now := time.Now().UTC()
	label := workers.WorkspaceIdentityLabelPrefix
	revision := workers.WorkspaceDigestLabelPrefix + "project"
	for _, worker := range []state.Worker{
		{ID: "owner", Capacity: 1, LastSeen: now, Labels: map[string]string{label + "project": "true", revision: digest, "private-label": "/private/environment"}},
		{ID: "disabled", LastSeen: now, Labels: map[string]string{label + "disabled": "true"}},
		{ID: "draining", Capacity: 1, Draining: true, LastSeen: now, Labels: map[string]string{label + "draining": "true"}},
		{ID: "missing", Capacity: 1, LastSeen: now, Labels: map[string]string{label + "missing": "false"}},
		{ID: "stale", Capacity: 1, LastSeen: now.Add(-2 * WorkerHeartbeatWindow), Labels: map[string]string{label + "stale": "true"}},
		{ID: "other", Capacity: 1, LastSeen: now, Labels: map[string]string{label + "offline": "true", label + "unconfigured": "true"}},
	} {
		if err := store.RegisterWorker(worker); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.ListWorkers(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	page := readWorkspaceCatalog(t, handler, "?limit=3")
	next := readWorkspaceCatalog(t, handler, "?limit=3&offset=3")
	if !page.HasMore || page.NextOffset != 3 || next.HasMore || next.NextOffset != 6 {
		t.Fatalf("unexpected paging: %+v %+v", page, next)
	}
	want := []WorkspaceCatalogEntry{
		{WorkspaceID: "disabled", WorkerID: "disabled", OwnerConnected: true, Status: "owner_unavailable"},
		{WorkspaceID: "draining", WorkerID: "draining", OwnerConnected: true, Status: "owner_unavailable"},
		{WorkspaceID: "missing", WorkerID: "missing", OwnerConnected: true, Status: "missing_advertised_binding"},
		{WorkspaceID: "offline", WorkerID: "offline", Status: "owner_offline"},
		{WorkspaceID: "project", WorkerID: "owner", Aliases: []string{"first", "second"}, DigestConfigured: true, Digest: digest, OwnerConnected: true, Status: "advertised"},
		{WorkspaceID: "stale", WorkerID: "stale", Status: "owner_offline"},
	}
	if got := append(page.Workspaces, next.Workspaces...); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected catalog: %+v", got)
	}
	if !reflect.DeepEqual(registry["project"].Aliases, []string{"second", "first"}) {
		t.Fatal("listing mutated configured aliases")
	}
	after, err := store.ListWorkers(time.Time{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("listing changed persisted workers: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator = NewCoordinator(store)
	coordinator.Workspaces = registry
	handler = WithAuth(NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator}), "admin", "")
	if got := readWorkspaceCatalog(t, handler, "").Workspaces; !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened catalog changed observations: %+v", got)
	}
	for _, advertised := range []string{"", "sha256:" + strings.Repeat("b", 64), digest} {
		_, err := coordinator.Register(WorkerRegistration{WorkerID: "owner", Capacity: 1, Labels: map[string]string{label + "project": "true", revision: advertised}})
		if err != nil {
			t.Fatal(err)
		}
		expectedStatus := "digest_mismatch"
		if advertised == digest {
			expectedStatus = "advertised"
		}
		row := readWorkspaceCatalog(t, handler, "?limit=1&offset=4").Workspaces[0]
		if row.Status != expectedStatus || row.Digest != digest || !row.OwnerConnected {
			t.Fatalf("advertisement %q reported as %+v", advertised, row)
		}
	}
	_, err = coordinator.Register(WorkerRegistration{
		WorkerID: "missing", Capacity: 1,
		Labels: map[string]string{label + "missing": "true", workers.WorkspaceDigestLabelPrefix + "missing": digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if row := readWorkspaceCatalog(t, handler, "?limit=1&offset=2").Workspaces[0]; row.Status != "digest_mismatch" || row.DigestConfigured || row.Digest != "" {
		t.Fatalf("unexpected advertised digest for an unpinned workspace: %+v", row)
	}
}

func TestWorkspaceCatalogBoundsAliasHeavyPages(t *testing.T) {
	store := state.New()
	coordinator := NewCoordinator(store)
	alias := strings.Repeat("a", workspaceCatalogRecordBudget*3/4)
	coordinator.Workspaces = workers.WorkspaceRegistry{
		"a": {WorkerID: "local", Path: "/private/a", Aliases: []string{alias + "a"}},
		"b": {WorkerID: "local", Path: "/private/b", Aliases: []string{alias, alias + "b"}},
		"c": {WorkerID: "local", Path: "/private/c", Aliases: []string{alias + "c"}},
	}
	handler := WithAuth(NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator}), "admin", "")
	first := readWorkspaceCatalog(t, handler, "")
	second := readWorkspaceCatalog(t, handler, "?offset="+strconv.Itoa(first.NextOffset))
	if len(first.Workspaces) != 2 || !first.HasMore || first.NextOffset != 2 || len(second.Workspaces) != 1 || second.HasMore {
		t.Fatalf("unexpected pages: %d %v %d, %d %v", len(first.Workspaces), first.HasMore, first.NextOffset, len(second.Workspaces), second.HasMore)
	}
	if row := first.Workspaces[1]; row.WorkspaceID != "b" || row.AliasesOmitted != 2 || len(row.Aliases) != 0 {
		t.Fatalf("oversized alias list hid the canonical workspace: %q (omitted %d)", row.WorkspaceID, row.AliasesOmitted)
	}
	if row := second.Workspaces[0]; row.WorkspaceID != "c" || len(row.Aliases) != 1 || row.AliasesOmitted != 0 {
		t.Fatalf("later workspace was unreachable: %q (omitted %d)", row.WorkspaceID, row.AliasesOmitted)
	}
}
