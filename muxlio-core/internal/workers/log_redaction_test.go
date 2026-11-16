package workers

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

type chunkChannel chan state.LogChunk

func (c chunkChannel) AppendLogChunk(chunk state.LogChunk) error {
	c <- chunk
	return nil
}

func TestLogSinksRedactAcrossFramesAndTimedFlushes(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			chunks := make(chunkChannel, 16)
			secrets := map[string]string{"api": "split-credential", "other": "other-secret"}
			var sink LogSink = NewStoreLogSink(chunks, "run", "act", secrets)
			if remote {
				sink = NewRemoteLogSink(RemoteLogSinkConfig{Secrets: secrets, Post: func(stream string, seq int, data []byte) error {
					return chunks.AppendLogChunk(state.LogChunk{Stream: stream, Seq: seq, Data: data})
				}})
			}
			defer sink.Close()
			appendText := func(stream, text string) {
				t.Helper()
				if err := sink.Append(stream, []byte(text)); err != nil {
					t.Fatal(err)
				}
			}
			read := func() state.LogChunk {
				t.Helper()
				select {
				case chunk := <-chunks:
					return chunk
				case <-time.After(3 * time.Second):
					t.Fatal("log flush did not arrive")
					return state.LogChunk{}
				}
			}
			appendText("agent", "ready ")
			if got := string(read().Data); got != "ready " {
				t.Fatalf("initial flush = %q", got)
			}
			appendText("agent", "output split-")
			// The 250 ms flush must release safe text while retaining the
			// potential credential, even if the next frame arrives later.
			if got := string(read().Data); got != "output " {
				t.Fatalf("timed flush exposed undecided text: %q", got)
			}
			appendText("stderr", "other-")
			appendText("agent", "cre")
			appendText("agent", "dential done")
			appendText("stderr", "secret!")
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			close(chunks)
			byStream := map[string]string{}
			for chunk := range chunks {
				byStream[chunk.Stream] += string(chunk.Data)
			}
			if byStream["agent"] != "[REDACTED] done" || byStream["stderr"] != "[REDACTED]!" {
				t.Fatalf("frame boundaries lost redaction: %+v", byStream)
			}
		})
	}
}

func TestLogRedactionPreservesOrdinarySuffixOnClose(t *testing.T) {
	chunks := make(chunkChannel, 16)
	sink := NewStoreLogSink(chunks, "run", "act", map[string]string{"api": "secret-value"})
	for _, text := range []string{"tests pas", "s"} {
		if err := sink.Append("agent", []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	close(chunks)
	var all strings.Builder
	for chunk := range chunks {
		all.Write(chunk.Data)
	}
	if got := all.String(); got != "tests pass" {
		t.Fatalf("ordinary suffix changed: %q", got)
	}
}

func TestAdapterPumpRedactsWorkerSecretsBeforeCallerSink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("adapter progress file descriptors require Unix")
	}
	chunks := make(chunkChannel, 16)
	// The sink predates execution-host secret resolution and knows no secrets.
	sink := NewStoreLogSink(chunks, "run", "act", nil)
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' '{"stream":"agent","text":"output split-"}' '{"stream":"agent","text":"credential done"}' >&3`)
	cleanup, err := startAdapterLogPump(cmd, ExecuteRequest{LogSink: sink, Secrets: map[string]string{"api": "split-credential"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	cleanup()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	close(chunks)
	var all strings.Builder
	for chunk := range chunks {
		all.Write(chunk.Data)
	}
	if got := all.String(); got != "output [REDACTED] done" {
		t.Fatalf("execution-host credential escaped the pump: %q", got)
	}
}
