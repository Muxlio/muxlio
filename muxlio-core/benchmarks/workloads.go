// Package main publishes reference workloads outside runtime internals.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/plan"
)

// Corpus is intentionally small enough to inspect. Each record represents an
// incident triage item; duplicates and punctuation exercise real parsing work.
var corpus = []string{
	"INC-01|payments|critical|Checkout unavailable for EU customers",
	"INC-02|search|warning|Index freshness exceeds ten minutes",
	"INC-03|payments|warning|Refund processing delayed",
	"INC-04|identity|critical|New sessions rejected",
	"INC-05|search|info|Index rebuild completed",
	"INC-06|storage|warning|Archive retrieval slow",
	"INC-07|identity|info|Signing key rotated",
	"INC-08|storage|critical|Uploads unavailable",
	"INC-09|payments|info|Settlement completed",
	"INC-10|search|warning|Query latency elevated",
	"INC-11|storage|info|Capacity increased",
	"INC-12|identity|warning|Password resets delayed",
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func literal(v any) plan.Binding { return plan.Binding{Expr: &plan.Expr{Op: "literal", Value: raw(v)}} }
func ref(node, port string) plan.Binding {
	return plan.Binding{Ref: &plan.ValueRef{Node: node, Port: port}}
}
func command(id, code string, inputs map[string]plan.Binding, templates ...string) plan.Node {
	args := []string{"-e", code}
	var substitutions []any
	substitutions = append(substitutions, nil, nil)
	for _, t := range templates {
		args = append(args, "")
		substitutions = append(substitutions, t)
	}
	return plan.Node{ID: id, Kind: plan.KindExecute, Executor: "command", Inputs: inputs,
		Params:  raw(map[string]any{"program": "node", "args": args, "argTemplates": substitutions, "trimStdout": true}),
		Outputs: map[string]plan.OutputDecl{"stdout": {}}, Retry: &plan.RetryPolicy{Effect: "pure", MaxAttempts: 1}}
}
func base(name string, nodes []plan.Node, output map[string]plan.Binding) *plan.Plan {
	return &plan.Plan{IRVersion: "1.0", Name: "reference-" + name, Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: nodes, Output: output}}}
}

const parseRecord = `const [id,service,severity,title]=process.argv[1].split('|');if(!id||!title)throw Error('bad incident');console.log(JSON.stringify({id,service,severity,title}))`
const countRecords = `const rows=JSON.parse(process.argv[1]);const counts={};for(const row of rows){const severity=row.split('|')[2];counts[severity]=(counts[severity]||0)+1}console.log(JSON.stringify(counts))`

func workload(name string, concurrency int, primary, secondary string) (*plan.Plan, error) {
	switch name {
	case "sequential", "recovery", "cancellation":
		first := command("triage", countRecords, map[string]plan.Binding{"records": literal(string(raw(corpus)))}, "{{records}}")
		last := command("report", `const x=JSON.parse(process.argv[1]);if(x.critical!==3||x.warning!==5||x.info!==4)throw Error('counts');console.log('verified incident report')`, map[string]plan.Binding{"counts": ref("triage", "stdout")}, "{{counts}}")
		nodes := []plan.Node{first}
		if name != "sequential" {
			nodes = append(nodes, plan.Node{ID: "checkpoint", Kind: plan.KindWait, WaitFor: "signal", SignalName: "continue", Inputs: map[string]plan.Binding{"after": ref("triage", "stdout")}})
			last.Inputs["checkpoint"] = ref("checkpoint", "payload")
		}
		nodes = append(nodes, last)
		return base(name, nodes, map[string]plan.Binding{"report": ref("report", "stdout")}), nil
	case "parallel-map":
		max := len(corpus)
		p := base(name, []plan.Node{{ID: "triage", Kind: plan.KindMap, Items: &plan.Expr{Op: "input"}, Key: &plan.Expr{Op: "item"}, BodyRef: "item", Concurrency: &concurrency, MaxItems: &max}}, map[string]plan.Binding{"records": ref("triage", "items")})
		p.Subgraphs["item"] = plan.Subgraph{Nodes: []plan.Node{command("parse", parseRecord, map[string]plan.Binding{"record": {Expr: &plan.Expr{Op: "item"}}}, "{{record}}")}, Output: map[string]plan.Binding{"record": ref("parse", "stdout")}}
		return p, nil
	case "large-artifact":
		// A 2 MiB incident export exercises actual file I/O and dependency transport.
		code := `const fs=require('node:fs');const row='INC-01|payments|critical|Checkout unavailable\n';let data=row.repeat(Math.ceil(2097152/row.length)).slice(0,2097152);fs.writeFileSync(process.env.MUXLIO_OUTPUT_DIR+'/incidents.txt',data)`
		produce := command("export", code, nil)
		produce.Outputs = map[string]plan.OutputDecl{"file": {Path: "incidents.txt", Mime: "text/plain"}}
		consume := command("checksum", `const fs=require('node:fs'),crypto=require('node:crypto');const b=fs.readFileSync(process.argv[1]);if(b.length!==2097152)throw Error('size');console.log(crypto.createHash('sha256').update(b).digest('hex'))`, map[string]plan.Binding{"file": ref("export", "file")}, "{{file.path}}")
		return base(name, []plan.Node{produce, consume}, map[string]plan.Binding{"checksum": ref("checksum", "stdout")}), nil
	case "mixed-provider", "coding", "agent-authored":
		if primary == "" || (name == "mixed-provider" && (secondary == "" || primary == secondary)) {
			return nil, fmt.Errorf("%s requires explicit agent executors (mixed-provider requires two distinct executors)", name)
		}
		task := "Triage the supplied incident corpus. Count incidents by severity. Write report.json with exactly the keys critical, warning and info, each containing the numeric count. Use the supplied records as the source of truth."
		path := "report.json"
		if name == "coding" {
			task = codingPrompt
			path = "triage.cjs"
		}
		if name == "agent-authored" {
			task = authorPrompt
			path = "workflow.json"
		}
		author := plan.Node{ID: "author", Kind: plan.KindExecute, Executor: primary, Task: task, Inputs: map[string]plan.Binding{"corpus": literal(string(raw(corpus)))}, Outputs: map[string]plan.OutputDecl{"file": {Path: path, Mime: "text/plain"}}}
		if name == "agent-authored" {
			return base(name, []plan.Node{author}, map[string]plan.Binding{"workflow": ref("author", "file")}), nil
		}
		nodes := []plan.Node{author}
		source := "author"
		if name == "mixed-provider" {
			reviewer := author
			reviewer.ID = "review"
			reviewer.Executor = secondary
			reviewer.Task = "Independently check the supplied incident corpus against the draft report. Correct mistakes and write report.json with exactly the severity keys critical, warning and info containing numeric counts. Do not trust the draft without counting the source records."
			reviewer.Inputs = map[string]plan.Binding{"draft": ref("author", "file"), "corpus": literal(string(raw(corpus)))}
			nodes = append(nodes, reviewer)
			source = "review"
		}
		check := `const fs=require('node:fs'),assert=require('node:assert/strict');const x=JSON.parse(fs.readFileSync(process.argv[1],'utf8'));assert.deepEqual(x,{critical:3,warning:5,info:4});console.log('verified incident report')`
		if name == "coding" {
			check = codingCheck
		}
		nodes = append(nodes, command("verify", check, map[string]plan.Binding{"file": ref(source, "file")}, "{{file.path}}"))
		return base(name, nodes, map[string]plan.Binding{"verification": ref("verify", "stdout")}), nil
	}
	return nil, fmt.Errorf("unknown workload %q", name)
}

const authorPrompt = `Author a runnable Muxlio Plan IR 1.0 workflow and write workflow.json. It must map over the root input array of strings, preserving order and all records, and expose the mapped records on a root output port named records. The engine unwraps single-port root and map-body outputs, so the run output is an array of the original strings. Only map and project node kinds are allowed, no executors. Use root="root", subgraphs object. Map: id="items", items={"op":"input"}, key={"op":"item"}, bodyRef="item", concurrency=2, maxItems=12. In the item subgraph use a project node id="copy", inputs={"record":{"expr":{"op":"item"}}}; subgraph output={"record":{"ref":{"node":"copy","port":"record"}}}. Root output={"records":{"ref":{"node":"items","port":"items"}}}. Include irVersion="1.0", name, root, subgraphs; omit digest. This is source generation: do not return a precomputed result.`

const codingPrompt = `Implement an incident export parser as a CommonJS module triage.cjs exporting summarize(records). Input is an array of strings id|service|severity|title. Trim every field. Only severities critical, warning, info are valid. Reject malformed rows, empty fields, duplicate incident IDs, non-arrays and non-string entries by throwing. Return {critical: count, warning: count, info: count}, including zero counts. Do not mutate input. The supplied corpus is an example; the verifier also uses unseen records and malformed inputs. Write the implementation, not a precomputed answer.`

const codingCheck = `const assert=require('node:assert/strict');const {summarize}=require(process.argv[1]);assert.equal(typeof summarize,'function');assert.deepEqual(summarize([]),{critical:0,warning:0,info:0});const rows=[' X | billing | critical | Failed checkout ','Y|search|warning|Slow query','Z|storage|info|Restored'];const original=[...rows];assert.deepEqual(summarize(rows),{critical:1,warning:1,info:1});assert.deepEqual(rows,original);assert.deepEqual(summarize(['N|search|warning|One','M|billing|warning|Two']),{critical:0,warning:2,info:0});for(const bad of [null,{},[1],['bad'],['A||info|title'],['|search|info|title'],['A|search|unknown|title'],['A|search|info|'],['A|search|info|title|extra'],['A|search|info|one','A|billing|warning|two']])assert.throws(()=>summarize(bad));console.log('verified incident parser')`
