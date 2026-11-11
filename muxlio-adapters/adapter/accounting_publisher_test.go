package adapter

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestAccountingPublisherCoalescesAndFinalFlushRecoversFailure(t *testing.T) {
	var mu sync.Mutex
	var delivered []uint64
	first := make(chan struct{})
	available := false
	p := newAccountingPublisher(time.Hour, func(a contract.Accounting) error {
		mu.Lock()
		defer mu.Unlock()
		if len(delivered) == 0 {
			delivered = append(delivered, a.Revision)
			close(first)
			return errors.New("offline")
		}
		if !available {
			return errors.New("offline")
		}
		delivered = append(delivered, a.Revision)
		return nil
	})
	if err := p.Publish(contract.Accounting{Revision: 1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first usage was not prompt")
	}
	for n := uint64(2); n <= 100; n++ {
		if err := p.Publish(contract.Accounting{Revision: n}, false); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	available = true
	mu.Unlock()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 2 || delivered[0] != 1 || delivered[1] != 100 {
		t.Fatalf("did not retain only latest pending full state: %v", delivered)
	}
}

func TestAccountingPublisherMaterialChangesBypassThrottleAndRejectConflicts(t *testing.T) {
	writes := make(chan uint64, 3)
	p := newAccountingPublisher(time.Hour, func(a contract.Accounting) error { writes <- a.Revision; return nil })
	defer p.Close()
	if err := p.Publish(contract.Accounting{Revision: 1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writes:
	case <-time.After(time.Second):
		t.Fatal("first usage delayed")
	}
	if err := p.Publish(contract.Accounting{Revision: 2}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-writes:
		if n != 2 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("material change was throttled")
	}
	if err := p.Publish(contract.Accounting{Revision: 2, Final: true}, true); err == nil {
		t.Fatal("conflicting revision accepted")
	}
	if err := p.Publish(contract.Accounting{Revision: 1}, true); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestAccountingPublisherMaterialRepublicationBypassesThrottle(t *testing.T) {
	writes := make(chan uint64, 4)
	p := newAccountingPublisher(time.Hour, func(a contract.Accounting) error { writes <- a.Revision; return nil })
	defer p.Close()
	if err := p.Publish(contract.Accounting{Revision: 1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writes:
	case <-time.After(time.Second):
		t.Fatal("first revision was not prompt")
	}
	if err := p.Publish(contract.Accounting{Revision: 2}, false); err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(contract.Accounting{Revision: 2}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-writes:
		if n != 2 {
			t.Fatalf("revision %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("material republication was throttled")
	}
}

func TestDeliveredMaterialRepublicationDoesNotExpediteFutureRevision(t *testing.T) {
	writes := make(chan uint64, 4)
	p := newAccountingPublisher(time.Hour, func(a contract.Accounting) error { writes <- a.Revision; return nil })
	defer p.Close()
	if err := p.Publish(contract.Accounting{Revision: 1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writes:
	case <-time.After(time.Second):
		t.Fatal("first usage was not prompt")
	}
	if err := p.Publish(contract.Accounting{Revision: 1}, true); err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(contract.Accounting{Revision: 2}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-writes:
		t.Fatalf("non-material revision %d was expedited", n)
	case <-time.After(30 * time.Millisecond):
	}
}
