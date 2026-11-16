package workers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

const muxlioLogFDEnv = "MUXLIO_LOG_FD"
const maxProgressFrameBytes = 128 << 10

var errFrameTooLong = errors.New("progress frame exceeds limit")

type muxlioLogLine = contract.Progress

// startAdapterLogPump wires MUXLIO_LOG_FD (fd 3) into req.LogSink while the adapter runs.
func startAdapterLogPump(cmd *exec.Cmd, req ExecuteRequest) (cleanup func(), err error) {
	if req.LogSink == nil && req.AccountingSink == nil {
		return func() {}, nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, writer)
	cmd.Env = append(cmd.Env, muxlioLogFDEnv+"=3")
	var progress *progressLogSink
	if req.LogSink != nil {
		progress = &progressLogSink{LogSink: req.LogSink, redactor: logRedactor{secrets: executionSecrets(req)}}
		req.LogSink = progress
	}

	stream := &drainingReader{file: reader}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer reader.Close()
		if progress != nil {
			defer progress.finish()
		}
		if req.AccountingSink != nil {
			defer func() {
				if err := req.AccountingSink.Close(); err != nil && req.LogSink != nil {
					_ = req.LogSink.Append("stderr", []byte("accounting final progress delivery incomplete"))
				}
			}()
		}
		br := bufio.NewReaderSize(stream, 64*1024)
		for {
			raw, err := readLimitedLine(br, maxProgressFrameBytes)
			if len(raw) > 0 {
				handleProgressFrame(req, raw)
			}
			if err == io.EOF {
				return
			}
			if errors.Is(err, errFrameTooLong) {
				if req.LogSink != nil {
					_ = req.LogSink.Append("stderr", []byte("skipped a progress frame larger than 128 KiB"))
				}
				continue
			}
			if err != nil {
				if req.LogSink != nil {
					_ = req.LogSink.Append("stderr", []byte("progress stream ended before later frames"))
				}
				return
			}
		}
	}()
	return func() {
		writer.Close()
		stream.exited()
		<-done
	}, nil
}

// adapterLogDrainGrace bounds how long the pump waits for more progress after
// the adapter exits. A detached descendant may still hold the inherited
// descriptor, so EOF alone cannot end the stream.
var adapterLogDrainGrace = time.Second

type drainingReader struct {
	file *os.File
	done atomic.Bool
}

// exited lets buffered frames drain while the stream stays active, then fails
// a read that waits longer than the grace period.
func (r *drainingReader) exited() {
	r.done.Store(true)
	_ = r.file.SetReadDeadline(time.Now().Add(adapterLogDrainGrace))
}

func (r *drainingReader) Read(p []byte) (int, error) {
	if r.done.Load() {
		_ = r.file.SetReadDeadline(time.Now().Add(adapterLogDrainGrace))
	}
	return r.file.Read(p)
}

func handleProgressFrame(req ExecuteRequest, raw []byte) {
	var line muxlioLogLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	variants := 0
	if line.OperationRef != nil {
		variants++
	}
	if line.Accounting != nil {
		variants++
	}
	if line.Stream != "" && line.Text != "" {
		variants++
	}
	if variants != 1 {
		return
	}
	if line.Accounting != nil {
		if req.AccountingSink != nil && line.Accounting.Validate() == nil {
			if err := req.AccountingSink.Append(*line.Accounting); err != nil && req.LogSink != nil {
				_ = req.LogSink.Append("stderr", []byte("accounting progress delivery incomplete"))
			}
		}
		return
	}
	if line.OperationRef != nil {
		if req.LogSink == nil {
			return
		}
		clean, err := sanitizeExecution(ExecuteResult{RemoteTask: line.OperationRef}, nil, executionSecrets(req))
		if err != nil {
			return
		}
		encoded, _ := json.Marshal(clean.RemoteTask)
		_ = req.LogSink.Append("operation", encoded)
		return
	}
	if req.LogSink == nil {
		return
	}
	_ = req.LogSink.Append(normalizeLogStream(line.Stream), []byte(RedactSecrets(line.Text, executionSecrets(req))))
}

// AdapterLogWriter returns the incremental log sink file adapters should write to.
func AdapterLogWriter() *os.File {
	value := os.Getenv(muxlioLogFDEnv)
	if value == "" {
		return nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 0 {
		return nil
	}
	return os.NewFile(uintptr(fd), "muxlio-log")
}

// ReadAdapterLogLines consumes newline-delimited muxlio log records until EOF.
func ReadAdapterLogLines(r io.Reader, sink LogSink) error {
	if sink == nil {
		return nil
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		raw, err := readLimitedLine(br, maxProgressFrameBytes)
		if errors.Is(err, errFrameTooLong) {
			continue
		}
		if err != nil && err != io.EOF {
			return err
		}
		if len(raw) > 0 {
			var line muxlioLogLine
			if json.Unmarshal(raw, &line) == nil {
				variants := 0
				if line.OperationRef != nil {
					variants++
				}
				if line.Accounting != nil {
					variants++
				}
				if line.Stream != "" && line.Text != "" {
					variants++
				}
				if variants == 1 && line.Accounting == nil {
					stream, data := normalizeLogStream(line.Stream), []byte(line.Text)
					if line.OperationRef != nil {
						stream = "operation"
						data, _ = json.Marshal(line.OperationRef)
					}
					if appendErr := sink.Append(stream, data); appendErr != nil {
						return appendErr
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// EncodeAdapterLogLine formats one incremental log record for MUXLIO_LOG_FD.
func EncodeAdapterLogLine(stream, text string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	return json.Marshal(muxlioLogLine{Stream: normalizeLogStream(stream), Text: text})
}

// readLimitedLine returns one line without its newline. A line longer than max
// is discarded through the next newline so later frames can still be read.
func readLimitedLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			if err == bufio.ErrBufferFull {
				for {
					_, err = r.ReadSlice('\n')
					if err == bufio.ErrBufferFull {
						continue
					}
					break
				}
			}
			if err != nil && err != io.EOF {
				return nil, err
			}
			return nil, errFrameTooLong
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return bytes.TrimRight(buf, "\r\n"), io.EOF
		}
		if err != nil {
			return nil, err
		}
		return bytes.TrimRight(buf, "\r\n"), nil
	}
}
