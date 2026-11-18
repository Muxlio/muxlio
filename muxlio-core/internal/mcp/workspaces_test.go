package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type workspaceCatalogRunner struct {
	api.Runner
	*api.Coordinator
}

func TestRemoteMCPListWorkspaces(t *testing.T) {
	store := state.New()
	coordinator := api.NewCoordinator(store)
	coordinator.Workspaces = workers.WorkspaceRegistry{
		"first":   {WorkerID: "local", Path: "/private/first"},
		"project": {WorkerID: "node", Path: "/private/project", Aliases: []string{"alias"}},
	}
	upstream := newTestServer(t, api.WithAuth(api.NewRouter(store, workspaceCatalogRunner{Coordinator: coordinator}), "admin", "worker",
		api.AccessGrant{Token: "catalog", Permissions: []string{"workspaces:read"}},
		api.AccessGrant{Token: "submitter", Permissions: []string{"submit"}},
	))
	handler, err := newHTTPHandler(&server{api: newAPIClient(upstream.URL, "admin")}, "listener-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"listener-token", "catalog", "submitter"} {
		t.Run(credential, func(t *testing.T) {
			response := httpMCPRequest(handler, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_workspaces","arguments":{"limit":1,"offset":1}}}`,
				map[string]string{"Authorization": "Bearer " + credential})
			var rpc struct {
				Result toolCallResult `json:"result"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rpc) != nil || len(rpc.Result.Content) != 1 {
				t.Fatalf("MCP response: %d %s", response.Code, response.Body)
			}
			result := rpc.Result.Content[0].Text
			if credential == "submitter" {
				if !rpc.Result.IsError || !strings.Contains(result, "MUXLIO_UNAUTHORIZED") || strings.Contains(result, "project") {
					t.Fatalf("unexpected authorization result: %s", result)
				}
				return
			}
			var page api.WorkspaceCatalogPage
			if rpc.Result.IsError || strings.Contains(result, "/private/") || json.Unmarshal([]byte(result), &page) != nil {
				t.Fatalf("invalid catalog: %s", result)
			}
			if page.Limit != 1 || page.Offset != 1 || page.NextOffset != 2 || page.HasMore || page.ObservedAt.IsZero() || len(page.Workspaces) != 1 {
				t.Fatalf("unexpected catalog page: %+v", page)
			}
			row := page.Workspaces[0]
			if row.WorkspaceID != "project" || row.Status != "owner_offline" || len(row.Aliases) != 1 || row.Aliases[0] != "alias" {
				t.Fatalf("unexpected workspace: %+v", row)
			}
		})
	}
	for _, tool := range tools {
		if tool.Name == "list_workspaces" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnly || tool.Annotations.Destructive {
				t.Fatalf("invalid catalog annotations: %+v", tool.Annotations)
			}
			return
		}
	}
	t.Fatal("list_workspaces missing from tools/list")
}

func TestListWorkspacesRejectsInvalidArguments(t *testing.T) {
	// No upstream client is needed when arguments fail validation.
	s := &server{}
	for _, args := range []string{`{"limit":0}`, `{"limit":201}`, `{"offset":-1}`, `{"offset":1.5}`, `{"path":"checkout"}`} {
		if result := s.toolListWorkspaces(json.RawMessage(args)); !result.IsError {
			t.Errorf("accepted invalid arguments %s", args)
		}
	}
}
