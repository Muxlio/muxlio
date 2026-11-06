package adapter

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/muxlio/muxlio-adapters/contract"
)

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
	raw = append(raw, '\n')
	_, _ = file.Write(raw)
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
		_, _ = file.Write(append(raw, '\n'))
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
	return os.NewFile(uintptr(fd), "muxlio-log")
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
	_, err = file.Write(append(raw, '\n'))
	return err
}
