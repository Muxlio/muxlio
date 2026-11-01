package state

import (
	"testing"
	"time"
)

func TestMemoryOutboxLifecycle(t *testing.T) {
	st := New()
	// Initially empty
	unpub, err := st.ListUnpublishedOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 0 {
		t.Fatalf("expected 0 unpublished, got %d", len(unpub))
	}

	// Enqueue records
	if err := st.EnqueueOutbox("msg-1", "assignment.created", map[string]any{"activationId": "act-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueOutbox("msg-2", "run.created", map[string]any{"runId": "run-1"}); err != nil {
		t.Fatal(err)
	}

	// Duplicate id fails
	if err := st.EnqueueOutbox("msg-1", "dup", nil); err == nil {
		t.Fatal("expected error on duplicate outbox id")
	}

	// List with limit
	unpub, err = st.ListUnpublishedOutbox(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 1 || unpub[0].ID != "msg-1" {
		t.Fatalf("expected 1 record (msg-1), got: %+v", unpub)
	}

	// Mark published
	if err := st.MarkOutboxPublished("msg-1", time.Now()); err != nil {
		t.Fatal(err)
	}

	unpub, err = st.ListUnpublishedOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 1 || unpub[0].ID != "msg-2" {
		t.Fatalf("expected msg-2 unpublished, got: %+v", unpub)
	}
}

