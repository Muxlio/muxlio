package workers

import "github.com/muxlio/muxlio-core/internal/state"

// RemoteLogSinkConfig posts bounded, throttled chunks to the coordinator.
type RemoteLogSinkConfig struct {
	Post    func(stream string, seq int, data []byte) error
	Secrets map[string]string
}

type remoteChunkStore struct {
	post func(string, int, []byte) error
}

func (s remoteChunkStore) AppendLogChunk(c state.LogChunk) error {
	if s.post == nil {
		return nil
	}
	return s.post(c.Stream, c.Seq, c.Data)
}

func NewRemoteLogSink(cfg RemoteLogSinkConfig) LogSink {
	return NewStoreLogSink(remoteChunkStore{cfg.Post}, "", "", cfg.Secrets)
}
