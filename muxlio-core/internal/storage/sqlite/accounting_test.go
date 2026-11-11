package sqlite

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"path/filepath"
	"testing"
)

func testAccounting(input, output, cache string) *contract.Accounting {
	m := func(v string) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(v), Source: "fixture", Fidelity: "metered"}}
	}
	return &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Input: m(input), Output: m(output), CacheRead: m(cache)}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
}
func assertAccountingCounts(t *testing.T, a *contract.Accounting, input, output, cache string) {
	t.Helper()
	if a == nil || len(a.Entries) != 1 || a.Entries[0].Tokens == nil {
		t.Fatalf("missing accounting: %+v", a)
	}
	v := a.Entries[0].Tokens
	if v.Input.Collected == nil || string(v.Input.Collected.Value) != input || v.Output.Collected == nil || string(v.Output.Collected.Value) != output || v.CacheRead.Collected == nil || string(v.CacheRead.Collected.Value) != cache {
		t.Fatalf("unexpected accounting: %+v", v)
	}
}

func TestAccountingSnapshotCASAndExactReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	snapshot := contract.Accounting{Revision: 1, Entries: []contract.AccountingEntry{{ID: "call", Credits: []contract.CreditMeasure{{Provider: "service", Unit: "credit", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0.18065595854063016", Source: "fixture", Fidelity: "metered"}}}}}}}
	if err = s.RecordAccountingSnapshot("attempt", snapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAccountingSnapshot("attempt", snapshot); err != nil {
		t.Fatal(err)
	}
	conflict := snapshot
	conflict.Final = true
	if err = s.RecordAccountingSnapshot("attempt", conflict); err == nil {
		t.Fatal("same revision conflict accepted")
	}
	snapshot.Revision = 2
	snapshot.Final = true
	if err = s.RecordAccountingSnapshot("attempt", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = 3
	if err = s.RecordAccountingSnapshot("attempt", snapshot); err == nil {
		t.Fatal("final snapshot mutated")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.ListExecutionAttempts("run", "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	a := rows[0].Accounting
	if a == nil || a.Revision != 2 || !a.Final || a.Entries[0].Credits[0].Amount.Collected.Value != "0.18065595854063016" {
		t.Fatalf("accounting changed: %+v", a)
	}
}

func TestFreshSchemaHasOnlyCanonicalAccounting(t *testing.T) {
	s := openTemp(t)
	rows, err := s.db.Query(`PRAGMA table_info(execution_attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, old := range []string{"usage_json", "measurements_json"} {
		if columns[old] {
			t.Fatalf("legacy column %s", old)
		}
	}
	for _, name := range []string{"accounting_json", "accounting_revision", "accounting_final", "accounting_hash", "accounting_assignment", "accounting_generation"} {
		if !columns[name] {
			t.Fatalf("missing %s", name)
		}
	}
}
