package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/muxlio/muxlio-core/internal/canvas"
)

func (s *server) toolCanvas(name string, args json.RawMessage) toolCallResult {
	switch name {
	case "list_canvas_components":
		cat := canvas.Catalog()
		type summary struct {
			Type        string `json:"type"`
			Version     int    `json:"version"`
			Category    string `json:"category"`
			Description string `json:"description"`
		}
		items := make([]summary, len(cat))
		for i, c := range cat {
			items[i] = summary{
				Type:        c.Type,
				Version:     c.Version,
				Category:    c.Category,
				Description: c.Description,
			}
		}
		bytes, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return errResult("failed to encode component list: " + err.Error())
		}
		return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(bytes)}}}

	case "get_canvas_component":
		var p struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return errResult("invalid arguments: " + err.Error())
		}
		if p.Type == "" {
			return errResult("type is required")
		}
		spec, err := canvas.GetComponent(p.Type)
		if err != nil {
			return errResult(err.Error())
		}
		bytes, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return errResult("failed to encode component specification: " + err.Error())
		}
		return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(bytes)}}}

	case "validate_canvas":
		var p struct {
			Content any    `json:"content"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return errResult("invalid arguments: " + err.Error())
		}

		var data []byte
		if p.Path != "" {
			clean := filepath.Clean(p.Path)
			var err error
			data, err = os.ReadFile(clean)
			if err != nil {
				return errResult("failed to read file: " + err.Error())
			}
		} else if p.Content != nil {
			switch c := p.Content.(type) {
			case string:
				data = []byte(c)
			default:
				var err error
				data, err = json.Marshal(c)
				if err != nil {
					return errResult("failed to serialize content: " + err.Error())
				}
			}
		} else {
			return errResult("either content or path is required")
		}

		res, valErr := canvas.Validate(data)
		out, _ := json.MarshalIndent(res, "", "  ")
		if valErr != nil || !res.Valid {
			return toolCallResult{
				IsError: true,
				Content: []contentBlock{{Type: "text", Text: string(out)}},
			}
		}
		return toolCallResult{
			Content: []contentBlock{{Type: "text", Text: string(out)}},
		}

	default:
		return errResult(fmt.Sprintf("unknown canvas tool %q", name))
	}
}
