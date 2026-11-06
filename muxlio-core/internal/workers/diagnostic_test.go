package workers

import (
	"errors"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"testing"
)

func TestMissingAdapterDiagnosticStable(t *testing.T) {
	executor := NewScriptExecutor(t.TempDir(), t.TempDir())
	var id string
	for i := 0; i < 2; i++ {
		_, err := executor.Execute(ExecuteRequest{Executor: "generic/missing"})
		var d *diagnostic.Diagnostic
		if !errors.As(err, &d) || d.Code != "MUXLIO_ADAPTER_NOT_FOUND" || d.Fix == "" {
			t.Fatalf("missing diagnostic: %v", err)
		}
		if i == 0 {
			id = d.ID
		} else if d.ID != id {
			t.Fatal("unstable identifier")
		}
	}
}
