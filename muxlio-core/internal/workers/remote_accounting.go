package workers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"sync"
	"time"
)

// RemoteAccountingSink retains one latest full snapshot until durable delivery
// succeeds. Retries use identical canonical bytes, and Close performs a bounded
// final flush. A later full snapshot can recover a failed earlier update.
type RemoteAccountingSink struct {
	AppendSnapshot func(contract.Accounting) error
	mu             sync.Mutex
	pending        *contract.Accounting
	pendingBytes   []byte
	delivered      uint64
	deliveredBytes []byte
	closed         bool
}

func (s *RemoteAccountingSink) Append(a contract.Accounting) error {
	raw, err := contract.CanonicalAccounting(a)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("accounting sink is closed")
	}
	if a.Revision < s.delivered {
		return fmt.Errorf("stale accounting delivery")
	}
	if a.Revision == s.delivered {
		if bytes.Equal(raw, s.deliveredBytes) {
			return nil
		}
		return fmt.Errorf("conflicting accounting delivery")
	}
	if s.pending != nil {
		if a.Revision < s.pending.Revision {
			return fmt.Errorf("stale pending accounting delivery")
		}
		if a.Revision == s.pending.Revision && !bytes.Equal(raw, s.pendingBytes) {
			return fmt.Errorf("conflicting pending accounting delivery")
		}
	}
	var copyA contract.Accounting
	if err = json.Unmarshal(raw, &copyA); err != nil {
		return err
	}
	s.pending = &copyA
	s.pendingBytes = append([]byte(nil), raw...)
	return s.flush()
}
func (s *RemoteAccountingSink) flush() error {
	if s.pending == nil {
		return nil
	}
	if s.AppendSnapshot == nil {
		return fmt.Errorf("accounting transport unavailable")
	}
	var err error
	for n := 0; n < 3; n++ {
		var copyA contract.Accounting
		if err = json.Unmarshal(s.pendingBytes, &copyA); err != nil {
			return err
		}
		if err = s.AppendSnapshot(copyA); err == nil {
			s.delivered = s.pending.Revision
			s.deliveredBytes = append([]byte(nil), s.pendingBytes...)
			s.pending = nil
			s.pendingBytes = nil
			return nil
		}
		if n < 2 {
			time.Sleep(time.Duration(n+1) * 100 * time.Millisecond)
		}
	}
	return err
}
func (s *RemoteAccountingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.flush()
}
func (s *RemoteAccountingSink) DeliveredRevision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered
}
