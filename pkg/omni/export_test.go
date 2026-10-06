// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

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

// Installation media mapping, exported for tests.
var (
	MediaTargetFromPreset      = mediaTargetFromPreset
	SchematicRequestFromPreset = schematicRequestFromPreset
	FilenameFromMediaURL       = filenameFromMediaURL
	ValidDiskImageFormat       = validDiskImageFormat
)
