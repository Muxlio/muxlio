package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestWorkflowVersionsAPI(t *testing.T) {
	st := state.New()
	router := NewRouter(st, nil)

	p := &plan.Plan{
		Name:      "test-wf",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "n1",
						Kind:     plan.KindExecute,
						Executor: "agent/claude",
					},
				},
			},
		},
	}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	p.Digest = digest
	planRaw, _ := json.Marshal(p)

	// 1. Upload workflow version
	postReq := httptest.NewRequest(http.MethodPost, "/v1/workflow-versions", bytes.NewReader(planRaw))
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", postRec.Code, postRec.Body.String())
	}

	var postResp map[string]string
	_ = json.Unmarshal(postRec.Body.Bytes(), &postResp)
	if postResp["digest"] != digest {
		t.Errorf("expected digest %s, got %s", digest, postResp["digest"])
	}

	// 2. Fetch workflow version by digest
	getReq := httptest.NewRequest(http.MethodGet, "/v1/workflow-versions/"+digest, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var fetched plan.Plan
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("failed to decode fetched plan: %v", err)
	}
	if fetched.Digest != digest || fetched.Name != "test-wf" {
		t.Errorf("fetched plan mismatch: %+v", fetched)
	}

	// 3. Nonexistent digest returns 404
	missingReq := httptest.NewRequest(http.MethodGet, "/v1/workflow-versions/nonexistent", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing version, got %d", missingRec.Code)
	}
}
