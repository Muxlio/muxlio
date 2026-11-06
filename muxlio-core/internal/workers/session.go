package workers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

const SessionEnvironmentLabel = "muxlio.dev/session-environment"

// SessionEnvironment identifies local state storage and operator configuration.
// No credential values or provider-specific details leave the worker.
func (s *ScriptExecutor) SessionEnvironment() string {
	host, _ := os.Hostname()
	home := s.AgentHome
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	raw, _ := json.Marshal([]any{host, s.workBase, s.adapterDir, home, s.Workspace, s.WorkerEnv, s.Executors})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func newSessionInstance() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", id)
}

type sessionRecord struct {
	Environment     string                 `json:"environment"`
	Handle          contract.SessionHandle `json:"handle"`
	ExecutorVersion string                 `json:"executorVersion"`
	Instance        string                 `json:"instance"`
	Workspace       string                 `json:"workspace"`
}

func (s *ScriptExecutor) sessionPath(executor, id string) string {
	key := sha256.Sum256([]byte(executor + "\x00" + id))
	return filepath.Join(s.workBase, "sessions", fmt.Sprintf("%x.json", key))
}

// executeSession fences local mutable state across attempts and processes.
// Removing the checkpoint before a nonmultiplexed prompt ensures a crashed or
// cancelled turn cannot later be presented as a known conversation checkpoint.
func (s *ScriptExecutor) executeSession(req ExecuteRequest) (ExecuteResult, error) {
	if req.Action == "steer" {
		// The adapter owns the live transport. Never acquire or mutate the turn
		// checkpoint while delivering an out-of-band message.
		if req.Session == nil || req.Session.Handle == nil {
			return ExecuteResult{Error: "steer requires an owned session"}, nil
		}
		h := req.Session.Handle
		owner := s.WorkerID
		if owner == "" {
			owner = "local:" + s.SessionEnvironment()
		}
		if h.OwnerWorker != owner || h.OwnerEnvironment != s.SessionEnvironment() {
			return ExecuteResult{Error: "session owner unavailable"}, nil
		}
		return s.execute(req)
	}
	if req.Session == nil {
		result, err := s.execute(req)
		if result.Session != nil {
			return ExecuteResult{Error: "adapter returned an unsolicited persistent session"}, nil
		}
		return result, err
	}
	manifest, err := s.Descriptor(req.Executor)
	if err != nil {
		return ExecuteResult{}, err
	}
	if err := CheckAdapterCapabilities(req, manifest); err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	req, err = ApplyWorkerWorkspace(req, s.Workspace)
	if err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return ExecuteResult{Error: err.Error()}, nil
		}
	}
	invocation, _ := json.Marshal([]any{req.Workspace, req.WorkspaceMode, params.Isolation})
	invocationEnvironment := fmt.Sprintf("%x", sha256.Sum256(invocation))
	owner := s.WorkerID
	if owner == "" {
		owner = "local:" + s.SessionEnvironment()
	}
	environment := s.SessionEnvironment()
	ctx := req.Context
	if ctx == nil {
		ctx = s.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req.Context = ctx
	var path string
	var release func()
	multiplex := manifest.Supports(contract.CapabilitySessionMultiplex)
	if req.Session.Mode == "resume" {
		handle := req.Session.Handle
		if handle.OwnerWorker != owner || handle.OwnerEnvironment != environment {
			return ExecuteResult{Error: "session owner worker or environment unavailable"}, nil
		}
		path = s.sessionPath(req.Executor, handle.ID)
		release, err = lockSession(ctx, path+".lock", multiplex)
		if err != nil {
			return ExecuteResult{Error: err.Error()}, nil
		}
		defer release()
		raw, readErr := os.ReadFile(path)
		var record sessionRecord
		if readErr != nil || json.Unmarshal(raw, &record) != nil || !record.Handle.Equal(*handle) || record.Environment != invocationEnvironment || record.ExecutorVersion != manifest.ExecutorVersion || record.Workspace != req.Workspace || (!handle.Recoverable && record.Instance != s.sessionInstance) {
			return ExecuteResult{Error: "session state missing, incompatible or unrecoverable; refusing to start a new conversation"}, nil
		}
		if !time.Now().Before(handle.ExpiresAt) {
			return ExecuteResult{Error: "session expired"}, nil
		}
		if !multiplex {
			if err := os.Remove(path); err != nil {
				return ExecuteResult{}, err
			}
		}
	}
	result, err := s.execute(req)
	fail := func(message string) (ExecuteResult, error) {
		if path != "" {
			_ = os.Remove(path)
		}
		result.Session = nil
		result.Error = message
		return result, err
	}
	if err != nil || result.Error != "" {
		return fail(result.Error)
	}
	if result.Session == nil {
		return fail("adapter did not return the requested session; conversation continuity unavailable")
	}
	handle := *result.Session
	handle.OwnerWorker, handle.OwnerEnvironment = owner, environment
	result.Session = &handle
	if err := handle.Validate(); err != nil {
		return fail(err.Error())
	}
	if handle.Executor != req.Executor || !time.Now().Before(handle.ExpiresAt) {
		return fail("adapter returned an incompatible or expired session")
	}
	if req.Session.Mode == "resume" && !handle.Equal(*req.Session.Handle) {
		return fail("adapter changed the resumed session; refusing false conversation continuity")
	}
	if req.Session.Mode == "create" {
		path = s.sessionPath(req.Executor, handle.ID)
		release, err = lockSession(ctx, path+".lock", false)
		if err != nil {
			result.Session = nil
			result.Error = err.Error()
			return result, nil
		}
		defer release()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			result.Session = nil
			result.Error = "adapter returned an existing session for create"
			return result, nil
		}
	}
	if multiplex && req.Session.Mode == "resume" {
		return result, nil
	}
	record := sessionRecord{Environment: invocationEnvironment, ExecutorVersion: manifest.ExecutorVersion, Handle: handle, Instance: s.sessionInstance, Workspace: req.Workspace}
	raw, err := json.Marshal(record)
	if err != nil {
		return fail(err.Error())
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return fail(err.Error())
	}
	return result, nil
}

// ExclusiveDispatchKeys includes implicit session reservations. The coordinator
// conservatively serializes even multiplex-capable adapters; local workers can
// use shared session locks only with an explicit adapter declaration.
func ExclusiveDispatchKeys(req ExecuteRequest) []string {
	keys := append([]string(nil), req.ConcurrencyKeys...)
	if req.Session != nil && req.Session.Mode == "resume" && req.Session.Handle != nil {
		h := req.Session.Handle
		raw, _ := json.Marshal([]string{h.Executor, h.OwnerWorker, h.OwnerEnvironment, h.ID})
		keys = append(keys, fmt.Sprintf("muxlio:session:%x", sha256.Sum256(raw)))
	}
	return keys
}

// MatchWorkerDispatch adds nonportable session ownership to generic matching.
func MatchWorkerDispatch(workerID, executor string, raw []byte, caps []state.WorkerCapability, labels map[string]string) bool {
	var req ExecuteRequest
	if json.Unmarshal(raw, &req) != nil {
		return false
	}
	if req.Session != nil && req.Session.Mode == "resume" {
		h := req.Session.Handle
		if h == nil || h.OwnerWorker != workerID || h.OwnerEnvironment != labels[SessionEnvironmentLabel] {
			return false
		}
	}
	return MatchDispatch(executor, raw, caps, labels)
}
