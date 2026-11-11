package workers

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/muxlio/muxlio-adapters/contract"
)

const muxlioLogFDEnv = "MUXLIO_LOG_FD"
const maxProgressFrameBytes = 128 << 10

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

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer reader.Close()
		if req.AccountingSink != nil {
			defer func() {
				if err := req.AccountingSink.Close(); err != nil && req.LogSink != nil {
					_ = req.LogSink.Append("stderr", []byte("accounting final progress delivery incomplete"))
				}
			}()
		}
		sc := bufio.NewScanner(reader)
		sc.Buffer(make([]byte, 64*1024), maxProgressFrameBytes)
		for sc.Scan() {
			var line muxlioLogLine
			if json.Unmarshal(sc.Bytes(), &line) != nil {
				continue
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
				continue
			}
			if line.Accounting != nil {
				if req.AccountingSink != nil && line.Accounting.Validate() == nil {
					if err := req.AccountingSink.Append(*line.Accounting); err != nil && req.LogSink != nil {
						_ = req.LogSink.Append("stderr", []byte("accounting progress delivery incomplete"))
					}
				}
				continue
			}
			if line.OperationRef != nil {
				if req.LogSink == nil {
					continue
				}
				clean, err := sanitizeExecution(ExecuteResult{RemoteTask: line.OperationRef}, nil, executionSecrets(req))
				if err != nil {
					continue
				}
				raw, _ := json.Marshal(clean.RemoteTask)
				_ = req.LogSink.Append("operation", raw)
			} else {
				if req.LogSink == nil {
					continue
				}
				_ = req.LogSink.Append(normalizeLogStream(line.Stream), []byte(RedactSecrets(line.Text, executionSecrets(req))))
			}
		}
	}()
	return func() {
		writer.Close()
		<-done
	}, nil
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
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxProgressFrameBytes)
	for sc.Scan() {
		var line muxlioLogLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
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
			continue
		}
		if line.Accounting != nil {
			continue
		} // this compatibility helper has no accounting sink
		stream, data := normalizeLogStream(line.Stream), []byte(line.Text)
		if line.OperationRef != nil {
			stream = "operation"
			data, _ = json.Marshal(line.OperationRef)
		}
		if err := sink.Append(stream, data); err != nil {
			return err
		}
	}
	return sc.Err()
}

// EncodeAdapterLogLine formats one incremental log record for MUXLIO_LOG_FD.
func EncodeAdapterLogLine(stream, text string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	return json.Marshal(muxlioLogLine{Stream: normalizeLogStream(stream), Text: text})
}
