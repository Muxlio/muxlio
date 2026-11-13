package contract

import (
	"reflect"
	"testing"
)

func TestSupportedOperationsRequireCompleteVersionedContract(t *testing.T) {
	tests := []struct {
		name string
		m    *AdapterManifest
		want []string
	}{
		{"legacy baseline", nil, []string{"execute"}},
		{"session v1", &AdapterManifest{Capabilities: map[string]bool{CapabilitySession: true}}, []string{"execute", "session.legacy"}},
		{"session v2", &AdapterManifest{Capabilities: map[string]bool{CapabilitySession: true}, CapabilityVersions: map[string]string{CapabilitySession: SessionVersion}}, []string{"execute", "session.create", "session.resume"}},
		{"incomplete remote", &AdapterManifest{Capabilities: map[string]bool{CapabilityRemoteTask: true}}, []string{"execute"}},
		{"remote", &AdapterManifest{Capabilities: map[string]bool{CapabilityRemoteTask: true, CapabilityReconcile: true}}, []string{"execute", "remote.submit", "remote.poll", "reconcile"}},
		{"unknown revision", &AdapterManifest{Capabilities: map[string]bool{CapabilityRemoteTask: true, CapabilityReconcile: true}, CapabilityVersions: map[string]string{CapabilityRemoteTask: "99"}}, []string{"execute", "reconcile"}},
		{"steer without persistent session", &AdapterManifest{Capabilities: map[string]bool{CapabilitySessionInput: true, CapabilityLiveInput: true}}, []string{"execute"}},
		{"persistent session without live input", &AdapterManifest{Capabilities: map[string]bool{CapabilitySession: true, CapabilitySessionInput: true}, CapabilityVersions: map[string]string{CapabilitySession: SessionVersion}}, []string{"execute", "session.create", "session.resume"}},
		{"steer", &AdapterManifest{Capabilities: map[string]bool{CapabilitySession: true, CapabilitySessionInput: true, CapabilityLiveInput: true}, CapabilityVersions: map[string]string{CapabilitySession: SessionVersion}}, []string{"execute", "session.create", "session.resume", "session.steer"}},
		{"incompatible contract", &AdapterManifest{ContractVersion: "99"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.SupportedOperations("generic/task"); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
