// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import "time"

// This file exports the internals the tests in the omni_test package exercise, so that those tests
// sit alongside the acceptance tests and consume the provider the way a caller would.

// InstallationMediaTarget is the medium an installation media preset resolves to.
type InstallationMediaTarget = installationMediaTarget

// Installation media identifiers.
const (
	PlatformMetal   = platformMetal
	DiskFormatRawXZ = diskFormatRawXZ

	InstallationMediaArchAMD64 = installationMediaArchAMD64
	InstallationMediaArchARM64 = installationMediaArchARM64

	InstallationMediaFormatISO = installationMediaFormatISO
	InstallationMediaFormatPXE = installationMediaFormatPXE
)

// Internals exported for tests.
var (
	MediaTargetFromPreset      = mediaTargetFromPreset
	SchematicRequestFromPreset = schematicRequestFromPreset
	FilenameFromMediaURL       = filenameFromMediaURL
	ValidDiskImageFormat       = validDiskImageFormat

	DescribeOneOf              = describeOneOf
	EnumDisplayName            = enumDisplayName
	EnumName                   = enumName
	EnumNames                  = enumNames
	JoinEndpoint               = joinEndpoint
	JoinURL                    = joinURL
	ParseJoinTokenExpiration   = parseJoinTokenExpiration
	PlannedJoinTokenExpiration = plannedJoinTokenExpiration
	ReconcileTimestamp         = reconcileTimestamp

	CheckJoinTokenCarriesLabels  = checkJoinTokenCarriesLabels
	ClusterNotReady              = clusterNotReady
	FindMachine                  = findMachine
	WaitForCluster               = waitForCluster
	WaitForMachine               = waitForMachine
	WaitForMachineToLeaveCluster = waitForMachineToLeaveCluster

	ErrMachineAmbiguous = errMachineAmbiguous
)

// SetWaitIntervals sets how often the waits re-check Omni without an event, and reopen broken
// watches, for a test, and restores them afterwards.
func SetWaitIntervals(t interface{ Cleanup(func()) }, resync, rewatch time.Duration) {
	previousResync, previousRewatch := waitResyncInterval, waitRewatchInterval
	waitResyncInterval, waitRewatchInterval = resync, rewatch

	t.Cleanup(func() { waitResyncInterval, waitRewatchInterval = previousResync, previousRewatch })
}
