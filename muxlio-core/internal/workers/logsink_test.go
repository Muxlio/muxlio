package workers

import (
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

type logRecorder struct {
	chunks []state.LogChunk
}

func (r *logRecorder) AppendLogChunk(c state.LogChunk) error {
	r.chunks = append(r.chunks, c)
	return nil
}

func TestStoreLogSinkPersistsIncrementalChunks(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run-1", "act-1", map[string]string{"secret": "xyzzy"})
	if err := sink.Append("agent", []byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 1 {
		t.Fatalf("expected first append to flush, got %d chunks", len(rec.chunks))
	}
	if err := sink.Append("agent", []byte("xyzzyworld")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 2 {
		t.Fatalf("chunks = %d", len(rec.chunks))
	}
	if got := string(rec.chunks[1].Data); got != "[REDACTED]world" {
		t.Fatalf("redacted data = %q", got)
	}
}

func TestReadAdapterLogLines(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run-1", "act-1", nil)
	input := "{\"stream\":\"agent\",\"text\":\"token\"}\n{\"text\":\"more\"}\n"
	if err := ReadAdapterLogLines(strings.NewReader(input), sink); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 2 || string(rec.chunks[0].Data) != "token" || string(rec.chunks[1].Data) != "more" {
		t.Fatalf("chunks=%+v", rec.chunks)
	}
}
