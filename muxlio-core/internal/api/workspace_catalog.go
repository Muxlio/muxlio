package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

const (
	workspaceCatalogPath       = "/v1/workspaces"
	workspaceCatalogPermission = "workspaces:read"
	// Bound alias-heavy pages as well as the number of records.
	workspaceCatalogRecordBudget = 512 << 10
)

// WorkspaceCatalogEntry is the public projection of a configured binding.
type WorkspaceCatalogEntry struct {
	WorkspaceID      string   `json:"workspaceId"`
	Aliases          []string `json:"aliases,omitempty"`
	AliasesOmitted   int      `json:"aliasesOmitted,omitempty"`
	WorkerID         string   `json:"workerId"`
	DigestConfigured bool     `json:"digestConfigured"`
	Digest           string   `json:"digest,omitempty"`
	OwnerConnected   bool     `json:"ownerConnected"`
	Status           string   `json:"status"`
}

type WorkspaceCatalogPage struct {
	Workspaces []WorkspaceCatalogEntry `json:"workspaces"`
	ObservedAt time.Time               `json:"observedAt"`
	Offset     int                     `json:"offset"`
	NextOffset int                     `json:"nextOffset"`
	HasMore    bool                    `json:"hasMore"`
	Limit      int                     `json:"limit"`
}

func handleWorkspaceCatalog(w http.ResponseWriter, r *http.Request, runner Runner) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
		return
	}
	if !CallerFromRequest(r).HasPermission(workspaceCatalogPermission) {
		writeError(w, http.StatusForbidden, CodeUnauthorized, "workspace catalog access requires workspaces:read")
		return
	}
	offset, limit := pageOptions(r)
	source, ok := runner.(interface {
		ListWorkspaces(int, int) (WorkspaceCatalogPage, error)
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, CodeStoreError, "workspace catalog unavailable")
		return
	}
	page, err := source.ListWorkspaces(offset, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeStoreError, "workspace catalog unavailable")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func newWorkspaceCatalogPage(offset, limit int) WorkspaceCatalogPage {
	return WorkspaceCatalogPage{Workspaces: []WorkspaceCatalogEntry{}, ObservedAt: time.Now().UTC(), Offset: offset, NextOffset: offset, Limit: limit}
}

// ListWorkspaces combines operator metadata with a snapshot of recent worker
// records. It neither checks the filesystem nor asks workers to do anything.
func (c *Coordinator) ListWorkspaces(offset, limit int) (WorkspaceCatalogPage, error) {
	offset, limit = normalizePageOptions(offset, limit)
	page := newWorkspaceCatalogPage(offset, limit)
	registry := c.workspaceRegistry()
	if err := registry.Validate(); err != nil {
		return page, err
	}
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ids = pageWindow(ids, offset, limit)
	page.HasMore = len(ids) > limit
	if page.HasMore {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return page, nil
	}

	owners := map[string]state.Worker{}
	if c.store != nil {
		recent, err := c.store.ListWorkers(page.ObservedAt.Add(-c.heartbeat))
		if err != nil {
			return page, err
		}
		for _, worker := range recent {
			owners[worker.ID] = worker
		}
	}
	budget := workspaceCatalogRecordBudget
	for _, id := range ids {
		binding := registry[id]
		if ValidateWorkerID(binding.WorkerID) != nil {
			return page, errors.New("invalid configured workspace owner")
		}
		owner, connected := owners[binding.WorkerID]
		entry := WorkspaceCatalogEntry{
			WorkspaceID: id, Aliases: slices.Clone(binding.Aliases), WorkerID: binding.WorkerID,
			DigestConfigured: binding.Digest != "", Digest: binding.Digest,
			OwnerConnected: connected, Status: workspaceCatalogStatus(id, binding, owner, connected),
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return page, err
		}
		// Keep the canonical ID discoverable when its aliases alone would
		// otherwise make this record impossible to page past.
		if len(raw)+1 > workspaceCatalogRecordBudget && len(entry.Aliases) > 0 {
			entry.AliasesOmitted = len(entry.Aliases)
			entry.Aliases = nil
			raw, err = json.Marshal(entry)
			if err != nil {
				return page, err
			}
		}
		budget -= len(raw) + 1
		if budget < 0 {
			if len(page.Workspaces) == 0 {
				return page, errors.New("workspace record exceeds catalog response size limit")
			}
			page.HasMore = true
			break
		}
		sort.Strings(entry.Aliases)
		page.Workspaces = append(page.Workspaces, entry)
	}
	page.NextOffset += len(page.Workspaces)
	return page, nil
}

func workspaceCatalogStatus(id string, binding workers.WorkspaceBinding, owner state.Worker, connected bool) string {
	if !connected {
		if binding.WorkerID == "local" {
			return "configured"
		}
		return "owner_offline"
	}
	if owner.Labels[workers.WorkspaceIdentityLabelPrefix+id] != "true" {
		return "missing_advertised_binding"
	}
	if owner.Labels[workers.WorkspaceDigestLabelPrefix+id] != binding.Digest {
		return "digest_mismatch"
	}
	if owner.Draining || owner.Capacity <= 0 {
		return "owner_unavailable"
	}
	return "advertised"
}
