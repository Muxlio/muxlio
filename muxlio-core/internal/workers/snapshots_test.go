package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestIntermediateSnapshotsSurviveWorkerScratchLossWithoutFinalPublication(t *testing.T) {
	dir := t.TempDir()
	driver, err := artifacts.NewLocal(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	client := artifacts.Client{BaseURL: "http://coordinator", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			json.NewEncoder(w).Encode(artifacts.TransferPlan{Mode: "proxy", URL: strings.TrimSuffix(r.URL.Path, "/transfer")})
		} else if r.Method == http.MethodPut {
			_, err := driver.PutStream(r.Context(), r.Body, "text/plain", r.Header.Get("X-Artifact-SHA256"))
			if err != nil {
				return nil, err
			}
		} else {
			stream, _, err := driver.GetStream(r.Context(), strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"))
			if err != nil {
				return nil, err
			}
			defer stream.Close()
			io.Copy(w, stream)
		}
		return w.Result(), nil
	})}}
	observed := make(chan state.ArtifactSnapshot, 2)
	var snapshots []state.ArtifactSnapshot
	var scratch string
	inner := cancellationTestExecutor(func(req ExecuteRequest) (ExecuteResult, error) {
		scratch = req.attemptScratch
		for _, text := range []string{"draft one", "draft two"} {
			// Publish each complete fixture atomically. A real in-place rewrite
			// may legitimately produce an additional empty or partial snapshot.
			if err := os.WriteFile(filepath.Join(scratch, "draft.tmp"), []byte(text), 0600); err != nil {
				return ExecuteResult{}, err
			}
			if err := os.Rename(filepath.Join(scratch, "draft.tmp"), filepath.Join(scratch, "report.txt")); err != nil {
				return ExecuteResult{}, err
			}
			select {
			case <-observed:
			case <-time.After(3 * time.Second):
				return ExecuteResult{Error: "snapshot timed out"}, nil
			}
		}
		return ExecuteResult{Error: "executor failed after producing drafts"}, nil
	})
	wrap := ArtifactTransferExecutor{Inner: inner, Client: client, WorkBase: filepath.Join(dir, "work"), SnapshotInterval: 5 * time.Millisecond,
		Snapshot: func(_ int, s state.ArtifactSnapshot) error {
			snapshots = append(snapshots, s)
			observed <- s
			return nil
		}}
	result, err := wrap.Execute(ExecuteRequest{RunID: "run", ActivationID: "act", AttemptID: "attempt", Outputs: map[string]OutputDecl{"report": {Path: "report.txt", Mime: "text/plain"}}})
	if err != nil || result.Error != "executor failed after producing drafts" || len(result.Outputs) != 0 {
		t.Fatalf("partial final output: %+v %v", result, err)
	}
	if len(snapshots) != 2 || snapshots[0].Digest == snapshots[1].Digest {
		t.Fatalf("immutable history: %+v", snapshots)
	}
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatal(err)
	}
	for i, snapshot := range snapshots {
		var data bytes.Buffer
		if err := client.Download(context.Background(), snapshot.Digest, snapshot.Size, &data); err != nil {
			t.Fatal(err)
		}
		if data.String() != []string{"draft one", "draft two"}[i] {
			t.Fatalf("snapshot changed: %q", data.String())
		}
	}
	// Corruption must fail even though the worker and its original files are gone.
	digest := snapshots[0].Digest
	if err := os.WriteFile(filepath.Join(dir, "blobs", digest[7:9], digest[9:]), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.Download(context.Background(), digest, snapshots[0].Size, io.Discard); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}

func TestProgressCoalescesChattyStreamsAndCapsBytes(t *testing.T) {
	rec := &logRecorder{}
	sink := NewStoreLogSink(rec, "run", "act", nil, "attempt")
	for range 10000 {
		if err := sink.Append("agent", []byte("token ")); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) > 3 {
		t.Fatalf("chatty stream was not batched: %d", len(rec.chunks))
	}
	for _, c := range rec.chunks {
		if c.AttemptID != "attempt" {
			t.Fatal("missing attempt provenance")
		}
	}
	rec = &logRecorder{}
	sink = NewStoreLogSink(rec, "run", "act", nil)
	for range 40 {
		if err := sink.Append("agent", bytes.Repeat([]byte("x"), 64<<10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range rec.chunks {
		total += len(c.Data)
	}
	if total != 1<<20 {
		t.Fatalf("progress budget: %d", total)
	}
}
