package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

type progressWrite struct {
	file  *os.File
	frame []byte
	done  chan error
}

var progressWriterOnce sync.Once
var progressWrites chan progressWrite
var progressFileMu sync.Mutex
var progressFile *os.File
var progressFD = -1

// A timed-out frame leaves the channel blocked; later frames fail fast until
// the writer drains, so terminal reporting is bounded by one timeout.
var progressStalled atomic.Bool
var accountingProgressMu sync.Mutex
var deliveredAccounting []byte

// AppendStreamText emits incremental assistant text to muxlio when MUXLIO_LOG_FD is set.
func AppendStreamText(text string) {
	if text == "" {
		return
	}
	file := logWriter()
	if file == nil {
		return
	}
	raw, err := json.Marshal(contract.Progress{Stream: "agent", Text: redact(text)})
	if err != nil {
		return
	}
	_ = writeProgress(file, raw)
}

// AppendLogChunk writes one bounded structured log record for executor output.
func AppendLogChunk(stream, text string) {
	if text == "" {
		return
	}
	file := logWriter()
	if file == nil {
		return
	}
	raw, err := json.Marshal(contract.Progress{Stream: stream, Text: redact(text)})
	if err == nil {
		_ = writeProgress(file, raw)
	}
}

// AppendAccountingSnapshot emits a typed full snapshot on the same serialized
// progress channel as logs and operation references.
func AppendAccountingSnapshot(snapshot contract.Accounting) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	file := logWriter()
	if file == nil {
		return fmt.Errorf("accounting progress channel unavailable")
	}
	payload, err := contract.CanonicalAccounting(snapshot)
	if err != nil {
		return err
	}
	accountingProgressMu.Lock()
	defer accountingProgressMu.Unlock()
	if bytes.Equal(payload, deliveredAccounting) {
		return nil
	}
	var canonical contract.Accounting
	if err = json.Unmarshal(payload, &canonical); err != nil {
		return err
	}
	raw, err := json.Marshal(contract.Progress{Accounting: &canonical})
	if err != nil {
		return err
	}
	if err = writeProgress(file, raw); err != nil {
		return err
	}
	deliveredAccounting = payload
	return nil
}

func writeProgress(file *os.File, raw []byte) error {
	progressWriterOnce.Do(func() {
		progressWrites = make(chan progressWrite, 32)
		go func() {
			for job := range progressWrites {
				n, err := job.file.Write(job.frame)
				if err == nil && n != len(job.frame) {
					err = io.ErrShortWrite
				}
				if len(progressWrites) == 0 {
					progressStalled.Store(false)
				}
				job.done <- err
			}
		}()
	})
	if progressStalled.Load() {
		return fmt.Errorf("progress delivery stalled")
	}
	job := progressWrite{file: file, frame: append(append([]byte(nil), raw...), '\n'), done: make(chan error, 1)}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case progressWrites <- job:
	case <-timer.C:
		progressStalled.Store(true)
		return fmt.Errorf("progress delivery timed out")
	}
	select {
	case err := <-job.done:
		return err
	case <-timer.C:
		progressStalled.Store(true)
		return fmt.Errorf("progress delivery timed out")
	}
}

// CaptureAgentText accumulates assistant output and mirrors it to the live log stream.
func CaptureAgentText(chunks *BoundedBuffer, text string) {
	if text == "" {
		return
	}
	_, _ = chunks.WriteString(text)
	AppendStreamText(text)
}

func logWriter() *os.File {
	value := os.Getenv("MUXLIO_LOG_FD")
	if value == "" {
		return nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 0 {
		return nil
	}
	progressFileMu.Lock()
	defer progressFileMu.Unlock()
	if progressFile == nil || progressFD != fd {
		progressFile = os.NewFile(uintptr(fd), "muxlio-log")
		progressFD = fd
	}
	return progressFile
}

// AppendOperationReference publishes the external operation identity as soon as
// it is known, before the adapter waits for completion. Missing delivery still
// leaves the coordinator conservative; it never proves that no effect occurred.
func AppendOperationReference(ref contract.RemoteTaskReference) error {
	file := logWriter()
	if file == nil {
		return nil
	}
	raw, err := json.Marshal(contract.Progress{OperationRef: &ref})
	if err != nil {
		return err
	}
	return writeProgress(file, raw)
}
