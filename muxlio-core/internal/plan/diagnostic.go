package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
)

func pointerPart(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// JSON token offsets point into the submitted plan, never a reserialized copy.
func sourceOffsets(raw []byte) map[string]int {
	offsets := map[string]int{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func(string)
	walk = func(path string) {
		offset := int(dec.InputOffset())
		for offset < len(raw) && (raw[offset] == ' ' || raw[offset] == '\n' || raw[offset] == '\r' || raw[offset] == '\t' || raw[offset] == ':' || raw[offset] == ',') {
			offset++
		}
		offsets[path] = offset
		t, err := dec.Token()
		if err != nil {
			return
		}
		if t == json.Delim('{') {
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return
				}
				walk(path + "/" + pointerPart(key.(string)))
			}
			_, _ = dec.Token()
		}
		if t == json.Delim('[') {
			for i := 0; dec.More(); i++ {
				walk(fmt.Sprintf("%s/%d", path, i))
			}
			_, _ = dec.Token()
		}
	}
	walk("")
	return offsets
}

func finalizeDiagnostics(p *Plan, errs []ValidationError) []ValidationError {
	offsets := sourceOffsets(p.sourceJSON)
	for i := range errs {
		d := &errs[i]
		pointer := ""
		if d.Subgraph != "" {
			pointer = "/subgraphs/" + pointerPart(d.Subgraph)
			if sg, ok := p.Subgraphs[d.Subgraph]; ok && d.NodeID != "" {
				for j, n := range sg.Nodes {
					if n.ID == d.NodeID {
						pointer += fmt.Sprintf("/nodes/%d", j)
						break
					}
				}
			}
		}
		if d.Source != nil {
			pointer = d.Source.Pointer
		}
		if d.Source == nil {
			fields := map[string]string{
				"MUXLIO_BAD_DEADLINE": "deadlineSeconds", "MUXLIO_MISSING_IR_VERSION": "irVersion",
				"MUXLIO_UNSUPPORTED_IR_VERSION": "irVersion", "MUXLIO_MISSING_NAME": "name",
				"MUXLIO_MISSING_ROOT_SUBGRAPH": "root", "MUXLIO_MISSING_EXECUTOR": "executor",
				"MUXLIO_MISSING_NODE_ID": "id", "MUXLIO_UNKNOWN_NODE_KIND": "kind",
				"MUXLIO_BAD_RETRY": "retry", "MUXLIO_MISSING_MAP_ITEMS": "items",
				"MUXLIO_MISSING_CONDITION": "condition", "MUXLIO_MISSING_THEN_REF": "thenRef",
				"MUXLIO_MISSING_MAP_BODY": "bodyRef", "MUXLIO_MISSING_LOOP_BODY": "bodyRef",
				"MUXLIO_MISSING_WHILE": "while", "MUXLIO_UNBOUNDED_LOOP": "maxIterations",
			}
			if field := fields[d.Code]; field != "" {
				pointer += "/" + field
			}
		}
		d.Source = sourcePosition(p.sourceJSON, pointer, offsets)
		if d.Fix == "" {
			d.Fix = "Correct the indicated plan field and rerun muxlio check --json."
		}
		d.Message = diagnostic.Redact(d.Message)
		d.Fix = diagnostic.Redact(d.Fix)
		d.Identify()
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if (errs[i].Severity == "error") != (errs[j].Severity == "error") {
			return errs[i].Severity == "error"
		}
		return errs[i].ID < errs[j].ID
	})
	if len(errs) > diagnostic.MaxIssues {
		truncated := diagnostic.New("MUXLIO_DIAGNOSTICS_TRUNCATED", "plan", "Additional diagnostics omitted.", "Fix these issues and rerun check.")
		if !HasErrors(errs) {
			truncated.Severity = "warning"
		}
		errs = append(errs[:diagnostic.MaxIssues-1], truncated)
	}
	return errs
}

func inputPointer(p *Plan, sg, node, port string) string {
	for i, n := range p.Subgraphs[sg].Nodes {
		if n.ID == node {
			return fmt.Sprintf("/subgraphs/%s/nodes/%d/inputs/%s/ref/node", pointerPart(sg), i, pointerPart(port))
		}
	}
	return ""
}

// Missing fields use their nearest existing parent for the line and column.
func sourcePosition(raw []byte, pointer string, offsets map[string]int) *diagnostic.Source {
	source := &diagnostic.Source{Pointer: pointer}
	parent := pointer
	for len(raw) > 0 {
		if offset, ok := offsets[parent]; ok {
			source.Line = bytes.Count(raw[:offset], []byte("\n")) + 1
			source.Column = offset - bytes.LastIndexByte(raw[:offset], '\n')
			break
		}
		slash := strings.LastIndex(parent, "/")
		if slash < 0 {
			break
		}
		parent = parent[:slash]
	}
	return source
}
