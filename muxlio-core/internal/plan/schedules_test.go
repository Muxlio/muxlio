package plan

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/schedule"
)

func TestValidateSchedulePinAndInput(t *testing.T) {
	p := &Plan{Name: "clock", IRVersion: "1.0", Root: "root", InputSchema: json.RawMessage(`{"type":"boolean"}`), Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{{ID: "task", Kind: KindExecute, Executor: "test/task"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	d := schedule.Definition{ID: "once", WorkflowDigest: digest, Input: json.RawMessage(`false`), Kind: "once", StartAt: time.Now(), Overlap: "allow", MaxCatchUp: 1}
	if err := ValidateSchedule(d, p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*schedule.Definition, *Plan)
	}{
		{"wrong input", func(d *schedule.Definition, p *Plan) { d.Input = json.RawMessage(`0`) }},
		{"missing input", func(d *schedule.Definition, p *Plan) { d.Input = nil }},
		{"unpinned", func(d *schedule.Definition, p *Plan) { p.Digest = "" }},
		{"different digest", func(d *schedule.Definition, p *Plan) { d.WorkflowDigest = "sha256:other" }},
		{"tampered bytes", func(d *schedule.Definition, p *Plan) { p.Name = "tampered" }},
		{"invalid kind", func(d *schedule.Definition, p *Plan) { d.Kind = "domain-event" }},
		{"zero interval", func(d *schedule.Definition, p *Plan) { d.Kind = "interval" }},
		{"negative interval", func(d *schedule.Definition, p *Plan) { d.Kind = "interval"; d.IntervalSeconds = -1 }},
		{"too much catchup", func(d *schedule.Definition, p *Plan) { d.MaxCatchUp = 1001 }},
		{"missing start", func(d *schedule.Definition, p *Plan) { d.StartAt = time.Time{} }},
		{"invalid ID", func(d *schedule.Definition, p *Plan) { d.ID = "path/name" }},
		{"unknown zone", func(d *schedule.Definition, p *Plan) {
			d.Kind = "cron"
			d.Cron = "0 0 * * *"
			d.Timezone = "Unknown/Zone"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, copy := d, *p
			tc.edit(&bad, &copy)
			if err := ValidateSchedule(bad, &copy); err == nil {
				t.Fatal("accepted invalid schedule")
			}
		})
	}
}
