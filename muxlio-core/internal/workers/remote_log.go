package workers

import (
	"bytes"
	"sync"
)

// RemoteLogSinkConfig posts flushed chunks through Post (typically to the coordinator).
type RemoteLogSinkConfig struct {
	Post    func(stream string, seq int, data []byte) error
	Secrets map[string]string
}

type remoteLogSink struct {
	cfg     RemoteLogSinkConfig
	mu      sync.Mutex
	seq     map[string]int
	pending map[string]*bytes.Buffer
}

// NewRemoteLogSink buffers redacted output and invokes Post for each persisted chunk.
func NewRemoteLogSink(cfg RemoteLogSinkConfig) LogSink {
	return &remoteLogSink{
		cfg:     cfg,
		seq:     make(map[string]int),
		pending: make(map[string]*bytes.Buffer),
	}
}

func (s *remoteLogSink) Append(stream string, data []byte) error {
	if s.cfg.Post == nil || len(data) == 0 {
		return nil
	}
	redacted := []byte(RedactSecrets(string(data), s.cfg.Secrets))
	s.mu.Lock()
	defer s.mu.Unlock()
	if stream == "" {
		stream = "agent"
	}
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

func (s *remoteLogSink) Close() error {
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

func (s *remoteLogSink) flushLocked(stream string, buf *bytes.Buffer) error {
	if buf.Len() == 0 {
		return nil
	}
	s.seq[stream]++
	data := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	return s.cfg.Post(stream, s.seq[stream], data)
}
