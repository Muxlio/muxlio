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
	secrets      map[string]string
	mu           sync.Mutex
	seq          map[string]int
	pending      map[string]*bytes.Buffer
}

// NewStoreLogSink persists redacted, bounded log chunks for one activation attempt.
func NewStoreLogSink(store logChunkStore, runID, activationID string, secrets map[string]string) LogSink {
	return &storeLogSink{
		store:        store,
		runID:        runID,
		activationID: activationID,
		secrets:      secrets,
		seq:          make(map[string]int),
		pending:      make(map[string]*bytes.Buffer),
	}
}

func (s *storeLogSink) Append(stream string, data []byte) error {
	if s == nil || s.store == nil || len(data) == 0 {
		return nil
	}
	if stream == "" {
		stream = "agent"
	}
	redacted := []byte(RedactSecrets(string(data), s.secrets))
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if buf.Len() > 0 {
		if err := s.flushLocked(stream, buf); err != nil {
			return err
		}
	}
	return nil
}

func (s *storeLogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		RunID:        s.runID,
		ActivationID: s.activationID,
		Stream:       stream,
		Seq:          s.seq[stream],
		Data:         append([]byte(nil), buf.Bytes()...),
		Timestamp:    time.Now(),
	}
	buf.Reset()
	if err := s.store.AppendLogChunk(chunk); err != nil {
		return err
	}
	return nil
}

// AppendBoundedExecutorLog stores the final executor diagnostics chunk (stream executor).
func AppendBoundedExecutorLog(store state.Store, runID, activationID string, attempt int, logs string, secrets map[string]string) error {
	if store == nil || logs == "" {
		return nil
	}
	logs = RedactSecrets(logs, secrets)
	const softMax = 60 * 1024
	if len(logs) > softMax {
		logs = logs[:softMax] + "\n[log truncated]"
	}
	return store.AppendLogChunk(state.LogChunk{
		RunID: runID, ActivationID: activationID, Stream: "executor", Seq: attempt,
		Data: []byte(logs), Timestamp: time.Now(),
	})
}

func normalizeLogStream(stream string) string {
	if stream == "" {
		return "agent"
	}
	return stream
}
