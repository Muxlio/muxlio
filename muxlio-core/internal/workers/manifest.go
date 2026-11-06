package workers

import (
	"fmt"
	"os"

	"github.com/muxlio/muxlio-adapters/contract"
)

type adapterManifest = contract.AdapterManifest

const (
	adapterCapabilityArtifacts  = contract.CapabilityArtifacts
	adapterCapabilityCancel     = contract.CapabilityCancel
	adapterCapabilityProgress   = contract.CapabilityProgress
	adapterCapabilitySession    = contract.CapabilitySession
	adapterCapabilityRemoteTask = contract.CapabilityRemoteTask
)

func loadAdapterManifest(scriptPath string) (*adapterManifest, error) {
	raw, err := os.ReadFile(scriptPath + ".json")
	if os.IsNotExist(err) {
		return contract.ParseManifest(nil)
	}
	if err != nil {
		return nil, fmt.Errorf("read adapter manifest: %w", err)
	}
	return contract.ParseManifest(raw)
}

// CheckAdapterCapabilities rejects unsupported features before starting the adapter.
func CheckAdapterCapabilities(req ExecuteRequest, manifest *adapterManifest) error {
	return (&contract.Request{
		Executor: req.Executor, ExecutorVersion: req.ExecutorVersion,
		ContractVersion: req.ContractVersion, RequiredCapabilities: req.RequiredCapabilities,
		SessionID: req.SessionID, RemoteTask: req.RemoteTask, Params: req.Params, Outputs: req.Outputs,
	}).Check(manifest)
}

// Descriptor exposes the shared contract to registration and coordinator clients.
func (s *ScriptExecutor) Descriptor(executor string) (*contract.AdapterManifest, error) {
	m, err := loadAdapterManifest(s.scriptPath(executor))
	if err != nil {
		return nil, err
	}
	if err := m.Check(executor, "", "", nil); err != nil {
		return nil, err
	}
	return m, nil
}
