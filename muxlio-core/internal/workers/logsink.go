package workers

import (
	"bytes"
	"sync"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

const logChunkMaxBytes = 64 << 10

// LogSink receives incremental executor output while an attempt is running.
type LogSink interface {
	Append(stream string, data []byte) error
	Close() error
}

type logChunkStore interface {
	AppendLogChunk(c state.LogChunk) error
}

type storeLogSink struct {
	store        logChunkStore
	runID        string
	activationID string
	attemptID    string
	secrets      map[string]string
	mu           sync.Mutex
	seq          map[string]int
	pending      map[string]*bytes.Buffer
	timer        *time.Timer
	closed       bool
	remaining    int
	lastFlush    time.Time
}

// NewStoreLogSink persists redacted, bounded log chunks for one activation attempt.
func NewStoreLogSink(store logChunkStore, runID, activationID string, secrets map[string]string, attemptID ...string) LogSink {
	s := &storeLogSink{
		store:        store,
		runID:        runID,
		activationID: activationID,
		secrets:      secrets,
		seq:          make(map[string]int),
		pending:      make(map[string]*bytes.Buffer),
		remaining:    state.MaxAttemptProgressBytes,
	}
	if len(attemptID) > 0 {
		s.attemptID = attemptID[0]
	}
	return s
}

func (s *storeLogSink) Append(stream string, data []byte) error {
	if s == nil || s.store == nil || len(data) == 0 {
		return nil
	}
	if stream == "operation" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return nil
		}
		s.seq[stream]++
		return s.store.AppendLogChunk(state.LogChunk{RunID: s.runID, ActivationID: s.activationID, AttemptID: s.attemptID, Stream: stream, Seq: s.seq[stream], Data: data, Timestamp: time.Now()})
	}
	stream = normalizeLogStream(stream)
	redacted := []byte(RedactSecrets(string(data), s.secrets))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.remaining == 0 {
		return nil
	}
	if len(redacted) > s.remaining {
		redacted = redacted[:s.remaining]
	}
	s.remaining -= len(redacted)
	buf := s.pending[stream]
	if buf == nil {
		buf = &bytes.Buffer{}
		s.pending[stream] = buf
	}
	for len(redacted) > 0 {
		room := logChunkMaxBytes - buf.Len()
		if room <= 0 {
			if err := s.flushLocked(stream, buf); err != nil {
				return err
			}
			room = logChunkMaxBytes
		}
		n := len(redacted)
		if n > room {
			n = room
		}
		_, _ = buf.Write(redacted[:n])
		redacted = redacted[n:]
		if buf.Len() >= logChunkMaxBytes {
			if err := s.flushLocked(stream, buf); err != nil {
				return err
			}
		}
	}
	if buf.Len() > 0 && time.Since(s.lastFlush) >= 250*time.Millisecond {
		if err := s.flushLocked(stream, buf); err != nil {
			return err
		}
	}
	if buf.Len() > 0 && s.timer == nil {
		s.timer = time.AfterFunc(250*time.Millisecond, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.timer = nil
			if !s.closed {
				for stream, pending := range s.pending {
					_ = s.flushLocked(stream, pending)
				}
			}
		})
	}
	return nil
}

func (s *storeLogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	for stream, buf := range s.pending {
		if buf.Len() == 0 {
			continue
		}
		if err := s.flushLocked(stream, buf); err != nil {
			return err
		}
	}
	return nil
}

func (s *storeLogSink) flushLocked(stream string, buf *bytes.Buffer) error {
	if buf.Len() == 0 {
		return nil
	}
	s.seq[stream]++
	chunk := state.LogChunk{
		AttemptID:    s.attemptID,
		RunID:        s.runID,
		ActivationID: s.activationID,
		Stream:       stream,
		Seq:          s.seq[stream],
		Data:         append([]byte(nil), buf.Bytes()...),
		Timestamp:    time.Now(),
	}
	if err := s.store.AppendLogChunk(chunk); err != nil {
		s.seq[stream]--
		return err
	}
	buf.Reset()
	s.lastFlush = time.Now()
	return nil
}

// AppendBoundedExecutorLog stores the final executor diagnostics chunk (stream executor).
func AppendBoundedExecutorLog(store state.Store, runID, activationID string, attempt int, logs string, secrets map[string]string, attemptID ...string) error {
	if store == nil || logs == "" {
		return nil
	}
	logs = RedactSecrets(logs, secrets)
	const softMax = 60 * 1024
	if len(logs) > softMax {
		logs = logs[:softMax] + "\n[log truncated]"
	}
	id := ""
	if len(attemptID) > 0 {
		id = attemptID[0]
	}
	return store.AppendLogChunk(state.LogChunk{
		AttemptID: id,
		RunID:     runID, ActivationID: activationID, Stream: "executor", Seq: attempt,
		Data: []byte(logs), Timestamp: time.Now(),
	})
}

func normalizeLogStream(stream string) string {
	switch stream {
	case "stdout", "stderr", "executor":
		return stream
	default:
		return "agent"
	}
}
