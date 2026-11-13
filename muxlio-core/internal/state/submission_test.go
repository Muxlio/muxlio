package state

import (
	"testing"
	"time"
)

func TestMemorySubmissionScopeAndRetention(t *testing.T) {
	s := New()
	for _, tc := range []struct{ workspace, key, id string }{{"a", "b\x00c", "one"}, {"a\x00b", "c", "two"}} {
		id, dup, err := s.CreateRunIdempotent(&Run{ID: tc.id, WorkspaceID: tc.workspace}, tc.key, "")
		if err != nil || dup || id != tc.id {
			t.Fatalf("distinct tuple: %s %v %v", id, dup, err)
		}
	}
	for _, id := range []string{"expired", "live", "pinned", "parent", "child", "effect"} {
		if _, _, err := s.CreateRunIdempotent(&Run{ID: id}, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{"expired", "pinned", "parent", "effect"} {
		s.runs[id].State = "succeeded"
		s.runs[id].UpdatedAt = old
	}
	s.runs["pinned"].Pinned = true
	s.runs["child"].Fork = &ForkProvenance{SourceRunID: "parent"}
	s.attempts = append(s.attempts, ExecutionAttempt{RunID: "effect", State: "failed", AwaitReconciliationReason: "uncertain effect"})
	for _, id := range []string{"live", "pinned", "parent", "child", "effect"} {
		got, dup, err := s.CreateRunIdempotent(&Run{ID: "new-" + id}, id, "")
		if err != nil || !dup || got != id {
			t.Fatalf("protected %s: %s %v %v", id, got, dup, err)
		}
	}
	got, dup, err := s.CreateRunIdempotent(&Run{ID: "replacement"}, "expired", "")
	if err != nil || dup || got != "replacement" {
		t.Fatalf("expired: %s %v %v", got, dup, err)
	}
}
