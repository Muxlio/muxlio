package workers

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestStoreLogSinkCutsOnRuneBoundaries(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run", "act", nil)
	if err := sink.Append("agent", []byte(strings.Repeat("a", logChunkMaxBytes-1)+"é")); err != nil {
		t.Fatal(err)
	}
	budget := state.MaxAttemptProgressBytes - logChunkMaxBytes - 1
	if err := sink.Append("agent", []byte(strings.Repeat("b", budget-1)+"é")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Append("agent", []byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, c := range rec.chunks {
		if !utf8.Valid(c.Data) {
			t.Fatalf("chunk %d splits a rune", c.Seq)
		}
		all.Write(c.Data)
	}
	if got := all.String(); strings.Contains(got, "z") || !strings.HasSuffix(got, "b") {
		t.Fatalf("budget cut resumed or split a rune: suffix %q", got[len(got)-4:])
	}
}

func TestStoreLogSinkRedactsOperationReferences(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run", "act", map[string]string{"api": "quoted\"credential"})
	if err := sink.Append("operation", []byte(`{"id":"quoted\"credential","url":"https://example.test/task"}`)); err != nil {
		t.Fatal(err)
	}
	var ref struct{ ID, URL string }
	if len(rec.chunks) != 1 || json.Unmarshal(rec.chunks[0].Data, &ref) != nil || ref.ID != "[REDACTED]" || ref.URL != "https://example.test/task" {
		t.Fatalf("operation reference not redacted: %+v", rec.chunks)
	}
}

func TestReadAdapterLogLines(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run-1", "act-1", nil)
	input := "{}\n{\"stream\":\"\",\"text\":\"\"}\n{\"stream\":\"agent\",\"text\":\"token\"}\n{\"text\":\"more\"}\n{}\n"
	if err := ReadAdapterLogLines(strings.NewReader(input), sink); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 1 || string(rec.chunks[0].Data) != "token" {
		t.Fatalf("chunks=%+v", rec.chunks)
	}
}

func TestReadAdapterLogLinesSkipsOversizedFrame(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run-1", "act-1", nil)
	input := strings.Repeat("x", 200*1024) + "\n{\"stream\":\"agent\",\"text\":\"kept\"}\n"
	if err := ReadAdapterLogLines(strings.NewReader(input), sink); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 1 || string(rec.chunks[0].Data) != "kept" {
		t.Fatalf("later frame was dropped: %+v", rec.chunks)
	}
}
