package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestNamedWorkflowLifecycle(t *testing.T) {
	store := state.New()
	router := WithAuth(NewRouter(store, nil), "admin", "worker",
		AccessGrant{Token: "submitter", Permissions: []string{"submit"}})
	call := func(method, path, token, body string, status int, code string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v1/"+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, status, w.Body)
		}
		if code != "" && decodeError(t, w).Code != code {
			t.Fatalf("wanted %s: %s", code, w.Body)
		}
		return w
	}
	read := func(w *httptest.ResponseRecorder) state.NamedWorkflow {
		t.Helper()
		var workflow state.NamedWorkflow
		if err := json.Unmarshal(w.Body.Bytes(), &workflow); err != nil {
			t.Fatal(err)
		}
		return workflow
	}
	assertSame := func(got, want state.NamedWorkflow) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workflow = %+v, want %+v", got, want)
		}
	}
	const collection = "workflows"
	const workflow = "workflows/example"
	firstPlan := `{"irVersion":"1.0","name":"Original name","root":"root","subgraphs":{"root":{"nodes":[]}}}`
	secondPlan := strings.Replace(firstPlan, "Original name", "New name", 1)
	create := fmt.Sprintf(`{"workflowId":"example","plan":%s}`, firstPlan)
	update := fmt.Sprintf(`{"expectedCurrentRevision":1,"plan":%s}`, secondPlan)

	for _, endpoint := range []struct{ method, path, body string }{
		{http.MethodPost, collection, create},
		{http.MethodGet, workflow, ""},
		{http.MethodPost, workflow, update},
	} {
		call(endpoint.method, endpoint.path, "", endpoint.body, 401, CodeUnauthorized)
		for _, token := range []string{"worker", "submitter"} {
			call(endpoint.method, endpoint.path, token, endpoint.body, 403, CodeUnauthorized)
		}
	}
	call(http.MethodGet, workflow, "admin", "", 404, CodeNotFound)
	// A direct registration remains usable before or after named publication.
	registration := call(http.MethodPost, "workflow-versions", "admin", firstPlan, 201, "")
	var direct struct{ Digest string }
	if err := json.Unmarshal(registration.Body.Bytes(), &direct); err != nil {
		t.Fatal(err)
	}
	call(http.MethodGet, workflow, "admin", "", 404, CodeNotFound)
	first := read(call(http.MethodPost, collection, "admin", create, 201, ""))
	if first.ID != "example" || first.CurrentRevision != 1 || first.CurrentDigest != direct.Digest ||
		len(first.Versions) != 1 || first.Versions[0].PublishedAt.IsZero() {
		t.Fatalf("initial publication: %+v", first)
	}
	assertSame(read(call(http.MethodPost, collection, "admin", create, 201, "")), first)
	call(http.MethodPost, collection, "admin", strings.Replace(create, firstPlan, secondPlan, 1), 409, CodeNamedWorkflowConflict)
	call(http.MethodDelete, "workflow-versions/"+first.CurrentDigest, "admin", "", 409, CodeWorkflowVersionCurrent)

	call(http.MethodPost, workflow, "admin", `{"plan":{}}`, 400, CodeBadRequest)
	call(http.MethodPost, workflow, "admin", `{"expectedCurrentRevision":1,"plan":{}}`, 422, CodeValidationError)
	assertSame(read(call(http.MethodGet, workflow, "admin", "", 200, "")), first)

	second := read(call(http.MethodPost, workflow, "admin", update, 200, ""))
	if second.ID != first.ID || second.CurrentRevision != 2 || second.CurrentDigest == first.CurrentDigest ||
		len(second.Versions) != 2 || second.Versions[0].Digest != second.CurrentDigest ||
		second.Versions[0].Name != "New name" || second.Versions[0].PublishedAt.IsZero() || second.Versions[1] != first.Versions[0] {
		t.Fatalf("second publication: %+v", second)
	}
	assertSame(read(call(http.MethodPost, workflow, "admin", update, 200, "")), second)
	assertSame(read(call(http.MethodGet, workflow, "admin", "", 200, "")), second)
	call(http.MethodPost, workflow, "admin", strings.Replace(update, secondPlan, firstPlan, 1), 409, CodeNamedWorkflowConflict)
	duplicateOld := fmt.Sprintf(`{"expectedCurrentRevision":2,"plan":%s}`, firstPlan)
	assertSame(read(call(http.MethodPost, workflow, "admin", duplicateOld, 200, "")), second)
	switchBack := fmt.Sprintf(`{"expectedCurrentRevision":2,"planDigest":%q}`, first.CurrentDigest)
	switched := read(call(http.MethodPost, workflow, "admin", switchBack, 200, ""))
	if switched.CurrentRevision != 3 || switched.CurrentDigest != first.CurrentDigest ||
		!reflect.DeepEqual(switched.Versions, []state.NamedWorkflowVersion{first.Versions[0], second.Versions[0]}) {
		t.Fatalf("switch back: %+v", switched)
	}
	assertSame(read(call(http.MethodPost, workflow, "admin", switchBack, 200, "")), switched)
	call(http.MethodPost, workflow, "admin", update, 409, CodeNamedWorkflowConflict)
	call(http.MethodDelete, "workflow-versions/"+second.CurrentDigest, "admin", "", 200, "")
	call(http.MethodPost, workflow, "admin", fmt.Sprintf(`{"expectedCurrentRevision":3,"planDigest":%q}`, second.CurrentDigest), 410, CodeWorkflowVersionRetired)
	call(http.MethodGet, "workflow-versions/"+second.CurrentDigest, "admin", "", 410, CodeWorkflowVersionRetired)
	switched.Versions[1].Retired = true
	assertSame(read(call(http.MethodGet, workflow, "admin", "", 200, "")), switched)
}
