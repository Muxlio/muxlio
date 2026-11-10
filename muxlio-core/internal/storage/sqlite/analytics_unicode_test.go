package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestQueryAnalyticsWorkflowFilterUnicodeParity(t *testing.T) {
	mem := state.New()
	defer mem.Close()
	sql := openTemp(t)
	wf := "café-Pipeline"
	at := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	run := &state.Run{ID: "u", WorkflowName: wf, State: "succeeded", CreatedAt: at, UpdatedAt: at}
	if err := mem.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := sql.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"café", "CAFÉ", "pipeline", "PIPE"} {
		query := state.AnalyticsQuery{Workflow: q, Limit: 10}
		assertAnalyticsParity(t, mem, sql, query)
		// Direct cross-check on SQLite vs Go for the filter predicate.
		var hit int
		if err := sql.db.QueryRow(`SELECT instr(go_lower(?), go_lower(?))`, wf, q).Scan(&hit); err != nil {
			t.Fatal(err)
		}
		goHit := strings.Contains(strings.ToLower(wf), strings.ToLower(q))
		if (hit > 0) != goHit {
			t.Fatalf("unicode filter mismatch wf=%q q=%q sqlite=%d go=%v", wf, q, hit, goHit)
		}
	}
}
