package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// multiSubgraphPlan returns a plan with three subgraphs ("alpha", "beta",
// "root") whose map keys are inserted in reverse alphabetical order, which
// exercises the non-determinism that existed before the sort fix.
func multiSubgraphPlan() *plan.Plan {
	return &plan.Plan{
		Name:      "test-workflow",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			// Inserted in reverse alpha order to surface map-iteration variance.
			"root": {
				Nodes: []plan.Node{
					{ID: "first", Kind: "execute", Executor: "runner/a"},
					{
						ID:   "second",
						Kind: "execute",
						Inputs: map[string]plan.Binding{
							// Two inputs referencing different nodes — their
							// dep list must be sorted regardless of map order.
							"z": {Ref: &plan.ValueRef{Node: "zebra", Port: "out"}},
							"a": {Ref: &plan.ValueRef{Node: "alpha-node", Port: "out"}},
						},
					},
				},
			},
			"beta": {
				Nodes: []plan.Node{
					{ID: "b1", Kind: "execute"},
				},
			},
			"alpha": {
				Nodes: []plan.Node{
					{ID: "a1", Kind: "execute"},
				},
			},
		},
	}
}

// captureRenderPlanGraph redirects stdout and returns the text written by
// renderPlanGraph for the given format.
func captureRenderPlanGraph(t *testing.T, p *plan.Plan, format string) string {
	t.Helper()
	old, r, w := captureStdout(t)
	defer func() { restoreStdout(old) }()
	renderPlanGraph(p, format)
	w.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

// captureStdout replaces os.Stdout with a pipe and returns (original, reader, writer).
func captureStdout(t *testing.T) (*os.File, *os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	return old, r, w
}

func restoreStdout(old *os.File) { os.Stdout = old }

// ---------- ASCII ----------

// TestRenderPlanGraphASCII_SubgraphOrder verifies that subgraphs are always
// rendered in sorted (alpha, beta, root) order regardless of Go map iteration.
func TestRenderPlanGraphASCII_SubgraphOrder(t *testing.T) {
	p := multiSubgraphPlan()

	// Run twice and assert both runs are identical and in sorted order.
	run1 := captureRenderPlanGraph(t, p, "ascii")
	run2 := captureRenderPlanGraph(t, p, "ascii")

	if run1 != run2 {
		t.Fatalf("ascii output differs between invocations:\nrun1:\n%s\nrun2:\n%s", run1, run2)
	}

	// alpha < beta < root
	idxAlpha := strings.Index(run1, `"alpha"`)
	idxBeta := strings.Index(run1, `"beta"`)
	idxRoot := strings.Index(run1, `"root"`)
	if idxAlpha < 0 || idxBeta < 0 || idxRoot < 0 {
		t.Fatalf("expected all three subgraph headings; got:\n%s", run1)
	}
	if !(idxAlpha < idxBeta && idxBeta < idxRoot) {
		t.Errorf("subgraphs not in sorted order: alpha=%d beta=%d root=%d\n%s", idxAlpha, idxBeta, idxRoot, run1)
	}
}

// TestRenderPlanGraphASCII_NodeOrderPreserved verifies that nodes within a
// subgraph appear in their original slice order (first, then second).
func TestRenderPlanGraphASCII_NodeOrderPreserved(t *testing.T) {
	p := multiSubgraphPlan()
	out := captureRenderPlanGraph(t, p, "ascii")

	idxFirst := strings.Index(out, "[first]")
	idxSecond := strings.Index(out, "[second]")
	if idxFirst < 0 || idxSecond < 0 {
		t.Fatalf("expected both nodes; got:\n%s", out)
	}
	if idxFirst > idxSecond {
		t.Errorf("node order not preserved: 'first' appears after 'second'\n%s", out)
	}
}

// TestRenderPlanGraphASCII_DepsAreSorted verifies that dependency names within
// a node are listed in sorted order.
func TestRenderPlanGraphASCII_DepsAreSorted(t *testing.T) {
	p := multiSubgraphPlan()
	out := captureRenderPlanGraph(t, p, "ascii")

	// "second" has deps [alpha-node, zebra] after sorting.
	idxAlphaNode := strings.Index(out, "alpha-node")
	idxZebra := strings.Index(out, "zebra")
	if idxAlphaNode < 0 || idxZebra < 0 {
		t.Fatalf("expected both dep names; got:\n%s", out)
	}
	if idxAlphaNode > idxZebra {
		t.Errorf("deps not sorted: 'alpha-node' should come before 'zebra'\n%s", out)
	}
}

// ---------- JSON ----------

func TestRenderPlanGraphJSON_Stable(t *testing.T) {
	p := multiSubgraphPlan()

	run1 := captureRenderPlanGraph(t, p, "json")
	run2 := captureRenderPlanGraph(t, p, "json")

	if run1 != run2 {
		t.Fatalf("json output differs between invocations:\nrun1:\n%s\nrun2:\n%s", run1, run2)
	}
}

func TestRenderPlanGraphJSON_SubgraphAndDepOrder(t *testing.T) {
	p := multiSubgraphPlan()
	out := captureRenderPlanGraph(t, p, "json")

	var result struct {
		Nodes []struct {
			ID        string   `json:"id"`
			Scope     string   `json:"scope"`
			DependsOn []string `json:"dependsOn"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}

	// Nodes should start with alpha subgraph, then beta, then root.
	if len(result.Nodes) < 4 {
		t.Fatalf("expected >=4 nodes, got %d", len(result.Nodes))
	}
	if result.Nodes[0].Scope != "alpha" {
		t.Errorf("first node scope = %q, want alpha", result.Nodes[0].Scope)
	}
	if result.Nodes[1].Scope != "beta" {
		t.Errorf("second node scope = %q, want beta", result.Nodes[1].Scope)
	}

	// Find the "second" node in "root" and verify its deps are sorted.
	for _, n := range result.Nodes {
		if n.ID == "root/second" {
			if len(n.DependsOn) != 2 {
				t.Fatalf("root/second DependsOn = %v, want 2 entries", n.DependsOn)
			}
			if n.DependsOn[0] != "alpha-node" || n.DependsOn[1] != "zebra" {
				t.Errorf("deps not sorted: got %v, want [alpha-node zebra]", n.DependsOn)
			}
			return
		}
	}
	t.Errorf("node root/second not found in JSON output")
}

// ---------- Mermaid ----------

func TestRenderPlanGraphMermaid_Stable(t *testing.T) {
	p := multiSubgraphPlan()

	run1 := captureRenderPlanGraph(t, p, "mermaid")
	run2 := captureRenderPlanGraph(t, p, "mermaid")

	if run1 != run2 {
		t.Fatalf("mermaid output differs between invocations:\nrun1:\n%s\nrun2:\n%s", run1, run2)
	}
}

func TestRenderPlanGraphMermaid_SubgraphOrder(t *testing.T) {
	p := multiSubgraphPlan()
	out := captureRenderPlanGraph(t, p, "mermaid")

	// alpha/a1 node should appear before beta/b1, which should appear before root/first.
	idxA := strings.Index(out, "alpha_a1")
	idxB := strings.Index(out, "beta_b1")
	idxR := strings.Index(out, "root_first")
	if idxA < 0 || idxB < 0 || idxR < 0 {
		t.Fatalf("missing expected mermaid node IDs:\n%s", out)
	}
	if !(idxA < idxB && idxB < idxR) {
		t.Errorf("mermaid nodes not in sorted subgraph order: alpha=%d beta=%d root=%d\n%s", idxA, idxB, idxR, out)
	}
}

// ---------- helpers ----------

// sortedSubgraphNames is also tested directly.
func TestSortedSubgraphNames(t *testing.T) {
	p := &plan.Plan{
		Subgraphs: map[string]plan.Subgraph{
			"z": {}, "a": {}, "m": {},
		},
	}
	got := sortedSubgraphNames(p)
	want := []string{"a", "m", "z"}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("sortedSubgraphNames[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestExtractNodeDependencies_Sorted(t *testing.T) {
	n := &plan.Node{
		Inputs: map[string]plan.Binding{
			"out1": {Ref: &plan.ValueRef{Node: "zebra", Port: "out"}},
			"out2": {Ref: &plan.ValueRef{Node: "aardvark", Port: "out"}},
			"out3": {Ref: &plan.ValueRef{Node: "mango", Port: "out"}},
		},
	}
	deps := extractNodeDependencies(n)
	want := []string{"aardvark", "mango", "zebra"}
	if len(deps) != len(want) {
		t.Fatalf("got %v, want %v", deps, want)
	}
	for i, d := range want {
		if deps[i] != d {
			t.Errorf("deps[%d] = %q, want %q", i, deps[i], d)
		}
	}
}

// ---------- sanitizeMermaidID ----------

// TestSanitizeMermaidID_QuoteStripped verifies that a double-quote in a node ID
// is replaced with an underscore. Without this fix the raw quote would appear in
// the Mermaid identifier slot (e.g. `root_node"id["..."]`) and break the diagram.
func TestSanitizeMermaidID_QuoteStripped(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`node"id`, `node_id`},
		{`root/my"node`, `root_my_node`},
		{`a"b"c`, `a_b_c`},
	}
	for _, tc := range cases {
		got := sanitizeMermaidID(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeMermaidID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestRenderPlanGraphMermaid_IDQuoteNotLeaked is an integration regression test:
// a node whose ID contains a double-quote must not produce a raw quote in the
// Mermaid identifier slot of the rendered output.
func TestRenderPlanGraphMermaid_IDQuoteNotLeaked(t *testing.T) {
	p := &plan.Plan{
		Name:      "id-quote-test",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: `node"id`, Kind: "execute"},
				},
			},
		},
	}
	out := captureRenderPlanGraph(t, p, "mermaid")
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Node declaration lines have the form: ID["label"]
		// The identifier part is everything before the first `["`.
		if idx := strings.Index(trimmed, `["`); idx >= 0 {
			identifier := trimmed[:idx]
			if strings.ContainsRune(identifier, '"') {
				t.Errorf("raw double-quote in Mermaid identifier slot: %q (full line: %q)", identifier, line)
			}
		}
	}
}

// ---------- escapeMermaidLabel ----------

func TestEscapeMermaidLabel_SafePassthrough(t *testing.T) {
	// Simple alphanumeric labels must be returned unchanged.
	cases := []string{
		"my-node",
		"execute",
		"runner/my-executor",
		"step_1",
		"succeeded",
	}
	for _, c := range cases {
		if got := escapeMermaidLabel(c); got != c {
			t.Errorf("escapeMermaidLabel(%q) = %q, want unchanged", c, got)
		}
	}
}

func TestEscapeMermaidLabel_DoubleQuote(t *testing.T) {
	// A double-quote in a node ID/kind/executor would close the Mermaid label
	// prematurely and must be escaped.
	input := `say "hello"`
	got := escapeMermaidLabel(input)
	if strings.Contains(got, `"`) {
		t.Errorf("escapeMermaidLabel(%q) still contains a raw double-quote: %q", input, got)
	}
	want := `say #quot;hello#quot;`
	if got != want {
		t.Errorf("escapeMermaidLabel(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMermaidLabel_HTMLMetacharacters(t *testing.T) {
	// & < > must be HTML-entity encoded so they do not inject raw HTML tags.
	cases := []struct {
		input string
		want  string
	}{
		{"a & b", "a &amp; b"},
		{"a < b", "a &lt; b"},
		{"a > b", "a &gt; b"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"a&b<c>d", "a&amp;b&lt;c&gt;d"},
	}
	for _, tc := range cases {
		got := escapeMermaidLabel(tc.input)
		if got != tc.want {
			t.Errorf("escapeMermaidLabel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestEscapeMermaidLabel_Newlines(t *testing.T) {
	// Newlines inside a Mermaid node declaration break the line and corrupt
	// the diagram. They must be replaced with spaces.
	cases := []struct {
		input string
	}{
		{"line1\nline2"},
		{"line1\r\nline2"},
		{"line1\rline2"},
	}
	for _, tc := range cases {
		got := escapeMermaidLabel(tc.input)
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("escapeMermaidLabel(%q) still contains a newline: %q", tc.input, got)
		}
	}
}

func TestEscapeMermaidLabel_Combined(t *testing.T) {
	// Simulate a pathological executor name with all metacharacters.
	input := "exec\"&<foo>\nbar"
	got := escapeMermaidLabel(input)
	if strings.ContainsAny(got, "\"\n\r") {
		t.Errorf("unsafe characters survive in: %q", got)
	}
	want := `exec#quot;&amp;&lt;foo&gt; bar`
	if got != want {
		t.Errorf("escapeMermaidLabel(%q) = %q, want %q", input, got, want)
	}
}

// ---------- Mermaid label integration ----------

// TestRenderPlanGraphMermaid_LabelEscaping verifies that user-supplied values
// containing Mermaid-unsafe characters are escaped in the rendered output while
// the structural HTML tokens (<br/>, <i>…</i>) are preserved verbatim.
func TestRenderPlanGraphMermaid_LabelEscaping(t *testing.T) {
	p := &plan.Plan{
		Name:      "escape-test",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       `node"id`,         // double-quote in ID
						Kind:     `kind<script>`,    // angle brackets
						Executor: `exec & "runner"`, // ampersand + quotes
					},
				},
			},
		},
	}

	out := captureRenderPlanGraph(t, p, "mermaid")

	// The raw dangerous characters must not appear in the output.
	if strings.Contains(out, `kind<script>`) {
		t.Errorf("unescaped '<script>' found in mermaid output:\n%s", out)
	}
	// Executor value must not contain unescaped double-quotes inside the label.
	// The outer wrapping quotes for the node syntax are fine, but the interior
	// quotes from "runner" must be escaped.
	if strings.Contains(out, `"runner"`) {
		t.Errorf("unescaped executor double-quotes found in mermaid output:\n%s", out)
	}
	// Structural HTML markup must survive.
	if !strings.Contains(out, "<br/>") {
		t.Errorf("structural <br/> tag missing from mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "<i>") {
		t.Errorf("structural <i> tag missing from mermaid output:\n%s", out)
	}
	// Escaped forms must be present.
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped &lt;script&gt; in mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("expected escaped &amp; in mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "#quot;") {
		t.Errorf("expected #quot; entity for escaped double-quote:\n%s", out)
	}
}

// TestRenderRunActivationsGraphMermaid_LabelEscaping verifies that activation
// nodeIDs and state strings with metacharacters are escaped in run graphs.
func TestRenderRunActivationsGraphMermaid_LabelEscaping(t *testing.T) {
	activations := []state.Activation{
		{
			ID:     "act-1",
			NodeID: `node<"evil">`,
			State:  `done & "ok"`,
		},
	}

	old, r, w := captureStdout(t)
	defer func() { restoreStdout(old) }()
	renderRunActivationsGraph(nil, activations, "mermaid")
	w.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if err != nil {
			break
		}
	}
	out := buf.String()

	if strings.Contains(out, `node<"evil">`) {
		t.Errorf("unescaped nodeID found in mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "&lt;") {
		t.Errorf("expected &lt; in mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "#quot;") {
		t.Errorf("expected #quot; entity in mermaid output:\n%s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("expected &amp; in mermaid output:\n%s", out)
	}
}
