package mcp

import (
	"bytes"
	"encoding/json"
	"net/url"
)

func collectionTool(name, description string, appendItem bool) toolDef {
	properties := map[string]any{
		"runId":      map[string]any{"type": "string"},
		"collection": map[string]any{"type": "string"},
	}
	required := []string{"runId", "collection"}
	if appendItem {
		properties["item"] = map[string]any{}
		required = append(required, "item")
	}
	return toolDef{
		Name:        name,
		Description: description,
		InputSchema: map[string]any{"type": "object", "properties": properties, "required": required},
	}
}

func (s *server) toolRunCollection(name string, args json.RawMessage) toolCallResult {
	var p struct {
		RunID      string          `json:"runId"`
		Collection string          `json:"collection"`
		Item       json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if p.RunID == "" || p.Collection == "" {
		return errResult("runId and collection are required")
	}
	path := "/v1/runs/" + url.PathEscape(p.RunID) + "/collections/" + url.PathEscape(p.Collection)
	switch name {
	case "get_run_collection":
		return collectionResult(s.api.get(path, nil))
	case "append_run_collection_item":
		if len(p.Item) == 0 {
			return errResult("item is required")
		}
		path += "/items"
	case "close_run_collection":
		path += "/close"
		p.Item = json.RawMessage(`{}`)
	}
	return collectionResult(s.api.post(path, p.Item))
}

func collectionResult(resp *apiResponse, err error) toolCallResult {
	if err != nil {
		return errResult(err.Error())
	}
	if resp.isError() {
		return apiResultOrError(resp)
	}
	if !json.Valid(resp.body) {
		return errResult("coordinator returned invalid or incomplete collection JSON")
	}
	// Keep the coordinator's compact JSON and exact number representations.
	return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(bytes.TrimSpace(resp.body))}}}
}
