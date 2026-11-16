package workers

import (
	"strings"
	"sync"
)

// logRedactor retains only a suffix that a later frame could complete into a
// credential. Flush timers must never publish this undecided text.
type logRedactor struct {
	secrets map[string]string
	tails   map[string]string
}

func (r *logRedactor) append(stream string, data []byte) []byte {
	if len(r.secrets) == 0 {
		return data
	}
	text := RedactSecrets(r.tails[stream]+string(data), r.secrets)
	keep := 0
	for _, secret := range r.secrets {
		for n := min(len(secret)-1, len(text)); n > keep; n-- {
			if strings.HasSuffix(text, secret[:n]) {
				keep = n
				break
			}
		}
	}
	if r.tails == nil {
		r.tails = make(map[string]string)
	}
	r.tails[stream] = text[len(text)-keep:]
	return []byte(text[:len(text)-keep])
}

func (r *logRedactor) finish(stream string) []byte {
	text := r.tails[stream]
	delete(r.tails, stream)
	// No future frame can complete a credential after closure. Preserve
	// ordinary text ending in a partial match, such as the last 's' in 'pass'.
	return []byte(text)
}

// progressLogSink protects worker-resolved credentials before they reach a
// caller's sink, which may have been created before secret handles resolved.
type progressLogSink struct {
	LogSink
	mu       sync.Mutex
	redactor logRedactor
}

func (s *progressLogSink) Append(stream string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stream == "operation" {
		return s.LogSink.Append(stream, redactOperation(data, s.redactor.secrets))
	}
	stream = normalizeLogStream(stream)
	if data = s.redactor.append(stream, data); len(data) > 0 {
		return s.LogSink.Append(stream, data)
	}
	return nil
}

// finish drains undecided suffixes without closing the caller-owned sink.
func (s *progressLogSink) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for stream := range s.redactor.tails {
		if data := s.redactor.finish(stream); len(data) > 0 {
			_ = s.LogSink.Append(stream, data)
		}
	}
}
