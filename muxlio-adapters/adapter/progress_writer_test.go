package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressWriterChild(t *testing.T) {
	if os.Getenv("MUXLIO_PROGRESS_CHILD") != "1" {
		return
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); AppendStreamText(strings.Repeat("x", 9000)) }()
		go func(i int) {
			defer wg.Done()
			a := contract.Accounting{Revision: uint64(i + 1), Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Total: contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: "0", Source: "fixture", Fidelity: "metered"}}}}}}
			if err := AppendAccountingSnapshot(a); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func TestBlockedProgressChild(t *testing.T) {
	if os.Getenv("MUXLIO_PROGRESS_BLOCKED") != "1" {
		return
	}
	file := logWriter()
	start := time.Now()
	err := writeProgress(file, bytes.Repeat([]byte("x"), 4<<20))
	if err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("blocked writer did not return bounded error: %v", err)
	}
}

func TestBlockedProgressCannotHangAdapter(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBlockedProgressChild$")
	cmd.Env = append(os.Environ(), "MUXLIO_PROGRESS_BLOCKED=1", "MUXLIO_LOG_FD=3")
	cmd.ExtraFiles = []*os.File{writer}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("blocked adapter: %v %s", err, out)
	}
}

func TestProgressFramesDoNotInterleave(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProgressWriterChild$")
	cmd.Env = append(os.Environ(), "MUXLIO_PROGRESS_CHILD=1", "MUXLIO_LOG_FD=3")
	cmd.ExtraFiles = []*os.File{writer}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 128<<10)
	textFrames, accountingFrames := 0, 0
	for scanner.Scan() {
		var p contract.Progress
		if err = json.Unmarshal(scanner.Bytes(), &p); err != nil {
			t.Fatal("interleaved progress frame")
		}
		if p.Accounting != nil {
			if _, err := contract.CanonicalAccounting(*p.Accounting); err != nil {
				t.Fatal(err)
			}
			accountingFrames++
		} else if p.Stream == "agent" && len(p.Text) == 9000 {
			textFrames++
		} else {
			t.Fatalf("invalid progress frame: %+v", p)
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("writer process: %v %s", err, stderr.String())
	}
	if textFrames != 20 || accountingFrames != 20 {
		t.Fatalf("lost progress frames: text=%d accounting=%d", textFrames, accountingFrames)
	}
}
