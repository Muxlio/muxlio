package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func handleRunCollection(w http.ResponseWriter, r *http.Request, store state.Store, runID, path string) {
	name, operation, _ := strings.Cut(path, "/")
	inspect := r.Method == http.MethodGet && path == name
	appendItem := r.Method == http.MethodPost && operation == "items"
	closeCollection := r.Method == http.MethodPost && operation == "close"
	if !inspect && !appendItem && !closeCollection {
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET collections[/:name] or POST collections/:name/{items,close}")
		return
	}
	run, err := store.GetRun(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	var pinned plan.Plan
	if len(run.PlanJSON) != 0 {
		if err := json.Unmarshal(run.PlanJSON, &pinned); err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
	}
	if inspect && name == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"collections": pinned.CollectionDeclarations(),
			"canAppend":   CallerFromRequest(r).HasPermission("inputs"),
		})
		return
	}
	node, ok := pinned.CollectionNode(name)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "run has no appendable collection with that name")
		return
	}
	spec, err := pinned.CollectionSpec(node, run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	action := state.CollectionAction{Close: closeCollection}
	if appendItem {
		var value any
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil || dec.Decode(new(any)) != io.EOF {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, "expected exactly one JSON item")
			return
		}
		item, err := node.CollectionItem(value)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, plan.CollectionItemValidationFailed, err.Error())
			return
		}
		action.Append = &item
		action.InvalidItem = node.ValidateCollectionItem(value)
	}
	collection, accepted, err := store.UpdateRunCollection(runID, spec, action)
	view := struct {
		state.RunCollection
		Open bool `json:"open"`
	}{collection, collection.ClosedReason == ""}
	switch {
	case errors.Is(err, state.ErrCollectionClosed):
		writeError(w, http.StatusConflict, "MUXLIO_COLLECTION_CLOSED", err.Error())
	case errors.Is(err, state.ErrCollectionConflict):
		writeError(w, http.StatusConflict, "MUXLIO_COLLECTION_ITEM_CONFLICT", err.Error())
	case errors.Is(err, state.ErrCollectionCapacity):
		writeError(w, http.StatusConflict, "MUXLIO_COLLECTION_CAPACITY", err.Error())
	case err != nil && err == action.InvalidItem:
		writeError(w, http.StatusUnprocessableEntity, plan.CollectionItemValidationFailed, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
	case appendItem:
		writeJSON(w, http.StatusOK, map[string]any{"collection": view, "accepted": accepted})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"collection": view})
	}
}
