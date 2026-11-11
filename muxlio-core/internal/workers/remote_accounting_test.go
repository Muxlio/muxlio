package workers

import (
	"errors"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"testing"
)

func TestAccountingDeliveryRetainsFailedSnapshotForFinalFlush(t *testing.T) {
	calls := 0
	var revisions []uint64
	sink := &RemoteAccountingSink{AppendSnapshot: func(a contract.Accounting) error {
		calls++
		revisions = append(revisions, a.Revision)
		if calls <= 3 {
			return errors.New("unavailable")
		}
		return nil
	}}
	a := testaccounting.Tokens(1, 0, 0)
	if err := sink.Append(*a); err == nil {
		t.Fatal("failed delivery acknowledged")
	}
	if sink.DeliveredRevision() != 0 {
		t.Fatal("failed revision marked durable")
	}
	a.Entries[0].Tokens.Input.Collected.Value = "999"
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.DeliveredRevision() != 1 || len(revisions) != 4 {
		t.Fatalf("final flush=%v", revisions)
	}
}
func TestAccountingDeliveryRejectsConflictsAndSupersedesWithFullState(t *testing.T) {
	available := false
	last := uint64(0)
	sink := &RemoteAccountingSink{AppendSnapshot: func(a contract.Accounting) error {
		if !available {
			return errors.New("offline")
		}
		last = a.Revision
		return nil
	}}
	a := testaccounting.Tokens(1, 0, 0)
	_ = sink.Append(*a)
	conflict := testaccounting.Tokens(2, 0, 0)
	if err := sink.Append(*conflict); err == nil {
		t.Fatal("conflicting revision accepted")
	}
	available = true
	conflict.Revision = 2
	if err := sink.Append(*conflict); err != nil {
		t.Fatal(err)
	}
	if last != 2 || sink.DeliveredRevision() != 2 {
		t.Fatal("new full state not delivered")
	}
	if err := sink.Append(*a); err == nil {
		t.Fatal("stale delivery accepted")
	}
}

func TestFailedFinalAccountingFlushCanBeRetried(t *testing.T) {
	available := false
	sink := &RemoteAccountingSink{AppendSnapshot: func(a contract.Accounting) error {
		if !available {
			return errors.New("offline")
		}
		return nil
	}}
	a := testaccounting.Tokens(1, 0, 0)
	if err := sink.Append(*a); err == nil {
		t.Fatal("failed append acknowledged")
	}
	if err := sink.Close(); err == nil {
		t.Fatal("failed final flush acknowledged")
	}
	if err := sink.Close(); err == nil {
		t.Fatal("repeated close hid pending failure")
	}
	available = true
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.DeliveredRevision() != a.Revision {
		t.Fatal("retry did not commit final snapshot")
	}
}
