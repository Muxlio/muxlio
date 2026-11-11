package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

// AccountingPublisher coalesces full snapshots without blocking the inference
// reader. One bounded writer sends first usage promptly, then at most once per
// two seconds, except material turn changes and the final flush.
type AccountingPublisher struct {
	mu                sync.Mutex
	pending           []byte
	pendingRevision   uint64
	delivered         []byte
	deliveredRevision uint64
	material          bool
	closed            bool
	err               error
	wake              chan struct{}
	stop              chan struct{}
	done              chan struct{}
	interval          time.Duration
	write             func(contract.Accounting) error
}

func NewAccountingPublisher() *AccountingPublisher {
	return newAccountingPublisher(2*time.Second, AppendAccountingSnapshot)
}

func newAccountingPublisher(interval time.Duration, write func(contract.Accounting) error) *AccountingPublisher {
	p := &AccountingPublisher{interval: interval, write: write, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *AccountingPublisher) Publish(a contract.Accounting, material bool) error {
	raw, err := contract.CanonicalAccounting(a)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("accounting publisher is closed")
	}
	revision, prior := p.deliveredRevision, p.delivered
	if p.pending != nil {
		revision, prior = p.pendingRevision, p.pending
	}
	if a.Revision < revision {
		return fmt.Errorf("stale accounting publication")
	}
	if a.Revision == revision {
		if !bytes.Equal(raw, prior) {
			return fmt.Errorf("conflicting accounting publication")
		}
		return nil
	}
	p.pending, p.pendingRevision = raw, a.Revision
	p.material = p.material || material
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return nil
}

func (p *AccountingPublisher) flush() error {
	p.mu.Lock()
	raw := append([]byte(nil), p.pending...)
	revision := p.pendingRevision
	p.material = false
	p.mu.Unlock()
	if len(raw) == 0 {
		return nil
	}
	var a contract.Accounting
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	err := p.write(a)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
	if err == nil {
		p.delivered, p.deliveredRevision = raw, revision
		if p.pendingRevision == revision {
			p.pending = nil
		}
	}
	return err
}

func (p *AccountingPublisher) run() {
	defer close(p.done)
	timer := time.NewTimer(p.interval)
	defer timer.Stop()
	var last time.Time
	for {
		select {
		case <-p.stop:
			for attempt := 0; attempt < 3; attempt++ {
				if err := p.flush(); err == nil {
					return
				}
			}
			return
		case <-p.wake:
		case <-timer.C:
		}
		p.mu.Lock()
		due := p.pending != nil && (p.material || last.IsZero() || time.Since(last) >= p.interval)
		p.mu.Unlock()
		if due {
			last = time.Now()
			_ = p.flush()
		}
		remaining := p.interval
		if !last.IsZero() && time.Since(last) < p.interval {
			remaining = p.interval - time.Since(last)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(remaining)
	}
}

// Close attempts the latest full state three times. The production transport
// bounds each write; the close deadline also bounds a broken transport callback.
func (p *AccountingPublisher) Close() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.stop)
	}
	p.mu.Unlock()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-timer.C:
		return fmt.Errorf("accounting final publication timed out")
	}
}

func FinalizeAccounting(r *accounting.Recorder, p *AccountingPublisher) (*contract.Accounting, error) {
	a, err := r.Finish()
	if err == nil {
		_ = p.Publish(*a, true)
	}
	// Terminal stdout delivery can recover a failed progress flush. Keep its
	// confirmed snapshot intact and report only a bounded progress diagnostic.
	if closeErr := p.Close(); closeErr != nil {
		AppendLogChunk("stderr", "accounting-progress-incomplete: terminal snapshot retained")
	}
	return a, err
}
