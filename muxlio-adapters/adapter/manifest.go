// Package adapter implements the shared executor wire contract.
package adapter

import "github.com/muxlio/muxlio-adapters/contract"

const ManifestVersion = contract.ManifestVersion
const (
	CapabilityCancel      = contract.CapabilityCancel
	CapabilityProgress    = contract.CapabilityProgress
	CapabilityArtifacts   = contract.CapabilityArtifacts
	CapabilitySession     = contract.CapabilitySession
	CapabilityRemoteTask  = contract.CapabilityRemoteTask
	CapabilityTypedResult = contract.CapabilityTypedResult
	CapabilityUsage       = contract.CapabilityUsage
)

type AdapterManifest = contract.AdapterManifest
type UnsupportedCapabilityError = contract.UnsupportedCapabilityError

var ParseManifest = contract.ParseManifest
