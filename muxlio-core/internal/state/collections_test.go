package state

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCollectionAppendAtIdleDeadline(t *testing.T) {
	deadline := time.Now()
	spec := CollectionSpec{MaxItems: 2, IdleTimeout: time.Second}
	item := CollectionItem{ID: "a", Value: json.RawMessage(`"value"`)}
	appendItem := CollectionAction{Append: &item}
	drained := 0
	checkDrained := CollectionAction{Drained: &drained}

	before := RunCollection{IdleDeadline: &deadline}
	if _, accepted, err := appendItem.Apply(&before, spec, "waiting", deadline.Add(-time.Nanosecond)); err != nil || !accepted {
		t.Fatalf("append before deadline: %v, %v", accepted, err)
	}
	if _, _, err := checkDrained.Apply(&before, spec, "waiting", deadline); err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].ID != item.ID || before.ClosedReason != "" || before.IdleDeadline != nil {
		t.Fatalf("pending accepted item could not drain: %+v", before)
	}

	at := RunCollection{IdleDeadline: &deadline}
	if changed, accepted, err := appendItem.Apply(&at, spec, "waiting", deadline); !changed || accepted || !errors.Is(err, ErrCollectionClosed) {
		t.Fatalf("append at deadline: %v, %v, %v", changed, accepted, err)
	}
	if at.ClosedReason != "idle_timeout" || len(at.Items) != 0 {
		t.Fatalf("timed-out collection: %+v", at)
	}

	cancelled := RunCollection{IdleDeadline: &deadline}
	if changed, accepted, err := appendItem.Apply(&cancelled, spec, "cancelled", deadline); changed || accepted || !errors.Is(err, ErrCollectionClosed) {
		t.Fatalf("append after cancellation: %v, %v, %v", changed, accepted, err)
	}
	if view := cancelled.View(spec, "cancelled", deadline); view.ClosedReason != "cancelled" || view.IdleDeadline != nil {
		t.Fatalf("cancelled collection: %+v", view)
	}
}
