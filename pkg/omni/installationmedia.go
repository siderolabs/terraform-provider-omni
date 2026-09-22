// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/omni/client/api/omni/management"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/meta"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/siderolabs/omni/client/pkg/omni/resources/virtual"
)

// Kind selectors accepted by the `format` attribute of the omni_installation_media data source.
//
// Anything else names a disk image format, which the image factory compresses on demand, so the
// suffix is a choice rather than a property of the medium. This mirrors the Talos provider's
// `talos_image_factory_urls.disk_image_format`, where it is also free-form.
//
// Like `omnictl media download --format`, `format` applies to bare-metal presets only: a cloud
// preset's format follows its platform and an SBC preset is always a raw disk image.
const (
	installationMediaFormatISO = "iso"
	installationMediaFormatPXE = "pxe"
)

// installationMediaFormatAliases are the shorthands `omnictl media download --format` accepts,
// mapped to the disk image suffix each one means.
//
// `raw` stays xz rather than following Talos >= 1.8's zstd default: changing it would hand back a
// different artifact, under a different filename, to configurations already written against it.
var installationMediaFormatAliases = map[string]string{
	"raw":   diskFormatRawXZ,
	"qcow2": "qcow2",
}

// platformMetal is the Talos platform bare-metal and SBC media are built for.
const platformMetal = "metal"

// diskFormatRawXZ is the disk image format Omni asks the factory for when a bare-metal preset is
// downloaded as a disk image, matching `omnictl media download --format raw`.
const diskFormatRawXZ = "raw.xz"

// resolveImageFactoryURL returns the image factory a preset for the given Talos version is served by.
//
// It mirrors what Omni itself does when picking a factory for a Talos version: a version served by
// a specific factory pins that factory. Anything else (an unset version, a version Omni does not
// know, or one with no factory recorded) falls back to the instance's primary factory. The URL is
// normalized the same way Omni normalizes it, so the server-side check that it is one of the
// configured factories compares equal.
func resolveImageFactoryURL(ctx context.Context, st cosistate.State, talosVersion string) (string, error) {
	if talosVersion != "" {
		version, err := safe.ReaderGetByID[*omni.TalosVersion](ctx, st, talosVersion)
		if err != nil && !cosistate.IsNotFoundError(err) {
			return "", fmt.Errorf("failed to look up Talos version %q: %w", talosVersion, err)
		}

		if version != nil {
			if url := version.TypedSpec().Value.GetImageFactoryUrl(); url != "" {
				return normalizeImageFactoryURL(url), nil
			}
		}
	}

	featuresConfig, err := safe.ReaderGetByID[*omni.FeaturesConfig](ctx, st, omni.FeaturesConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to look up the Omni features config: %w", err)
	}

	return normalizeImageFactoryURL(featuresConfig.TypedSpec().Value.GetImageFactoryBaseUrl()), nil
}

// schematicRequestFromPreset builds the schematic request that produces a preset's media.
//
// It mirrors `createSchematic` and `BuildParamsFromPreset` in Omni's own
// client/pkg/omnictl/internal/download, which is an internal package and cannot be imported.
//
// The Talos version is passed through verbatim, empty included: Omni substitutes its own default for
// an empty version, which is a better answer than any default this provider could carry, since a
// constant compiled in here would drift from the server's.
//
// ImageFactoryUrl is deliberately not set, even though the preset records one. Setting it makes
// CreateSchematic pick the factory with ForURL, while GetInstallationMediaURL has no such field and
// resolves the factory from the Talos version, so on an instance with a secondary factory the
// schematic could be registered with one factory and the URL built against another. Leaving it unset
// puts both calls on the same version-driven rule, which Omni keeps aligned on purpose: a version
// Omni does not know about counts as primary, the same fallback ForTalosVersion makes.
func schematicRequestFromPreset(
	ctx context.Context, st cosistate.State, spec *specs.InstallationMediaConfigSpec,
) (*management.CreateSchematicRequest, error) {
	request := &management.CreateSchematicRequest{
		TalosVersion:             spec.GetTalosVersion(),
		Extensions:               spec.GetInstallExtensions(),
		JoinToken:                spec.GetJoinToken(),
		SiderolinkGrpcTunnelMode: schematicGRPCTunnelMode(spec.GetGrpcTunnel()),
		Bootloader:               spec.GetBootloader(),
		EmbeddedMachineConfig:    spec.GetEmbeddedMachineConfig(),
		MetaValues:               map[uint32]string{},
	}

	if kernelArgs := spec.GetKernelArgs(); kernelArgs != "" {
		request.ExtraKernelArgs = strings.Fields(kernelArgs)
	}

	// Initial machine labels travel in the Talos META partition under the key Omni reads them from,
	// so the encoding comes from Omni's own package rather than being reproduced here.
	if labels := spec.GetMachineLabels(); len(labels) > 0 {
		encoded, err := meta.ImageLabels{Labels: labels}.Encode()
		if err != nil {
			return nil, fmt.Errorf("failed to encode the preset's machine labels: %w", err)
		}

		request.MetaValues[meta.LabelsMeta] = string(encoded)
	}

	// MediaId is deliberately left unset. It exists so the server can derive an overlay from an
	// InstallationMedia resource; a preset carries its own overlay, and an empty ID yields no overlay.
	if sbc := spec.GetSbc(); sbc != nil {
		config, err := safe.ReaderGetByID[*virtual.SBCConfig](ctx, st, sbc.GetOverlay())
		if err != nil {
			return nil, fmt.Errorf("failed to look up the SBC config for overlay %q: %w", sbc.GetOverlay(), err)
		}

		request.Overlay = &management.CreateSchematicRequest_Overlay{
			Name:    config.TypedSpec().Value.GetOverlayName(),
			Image:   config.TypedSpec().Value.GetOverlayImage(),
			Options: sbc.GetOverlayOptions(),
		}
	}

	return request, nil
}

// validDiskImageFormat reports whether a disk image suffix is one the image factory can serve.
//
// It mirrors the server's own path-segment rule, since the suffix becomes a path segment of the
// media URL: rejecting it here turns a malformed format into a plan-time error rather than a
// failed API call.
func validDiskImageFormat(format string) bool {
	if format == "" || format == "." || format == ".." {
		return false
	}

	return strings.IndexFunc(format, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '.', r == '_', r == '-':
			return false
		default:
			return true
		}
	}) == -1
}

// secondaryImageFactoryConfigured reports whether the instance has a second image factory.
//
// It matters only for an unpinned preset: CreateSchematic resolves an empty Talos version to the
// primary factory, while the URL lookup substitutes Omni's default version and uses whichever factory
// serves it. With one factory configured they cannot disagree.
func secondaryImageFactoryConfigured(ctx context.Context, st cosistate.State) (bool, error) {
	featuresConfig, err := safe.ReaderGetByID[*omni.FeaturesConfig](ctx, st, omni.FeaturesConfigID)
	if err != nil {
		return false, fmt.Errorf("failed to look up the Omni features config: %w", err)
	}

	return featuresConfig.TypedSpec().Value.GetSecondaryImageFactoryBaseUrl() != "", nil
}

// installationMediaTarget identifies the medium to ask Omni for.
//
// Omni assembles the image factory filename out of these, so nothing here encodes the factory's
// naming convention.
// The fields are exported so the tests in the omni_test package can assert on them through the
// alias in export_test.go; the type itself stays unexported, so none of this reaches consumers.
type installationMediaTarget struct {
	Platform     string
	Architecture string
	Format       string
	Kind         management.InstallationMediaURLRequest_InstallationMediaKind
	SecureBoot   bool
}

// mediaTargetFromPreset maps a preset and the requested format onto the medium to ask Omni for.
//
// The format applies to bare-metal presets only, exactly as `omnictl media download` treats it: a
// cloud preset's format is implied by its platform, and an SBC preset is always a raw disk image.
func mediaTargetFromPreset(
	ctx context.Context, st cosistate.State, spec *specs.InstallationMediaConfigSpec, format string,
) (installationMediaTarget, error) {
	media := installationMediaTarget{
		Architecture: architectureName(spec.GetArchitecture()),
		SecureBoot:   spec.GetSecureBoot(),
	}

	switch {
	case spec.GetCloud() != nil:
		if format != "" {
			return media, fmt.Errorf(
				"format is not applicable to the cloud preset for platform %q: a cloud preset's image format follows its platform",
				spec.GetCloud().GetPlatform())
		}

		config, err := safe.ReaderGetByID[*virtual.CloudPlatformConfig](ctx, st, spec.GetCloud().GetPlatform())
		if err != nil {
			return media, fmt.Errorf("failed to look up the cloud platform config for %q: %w", spec.GetCloud().GetPlatform(), err)
		}

		media.Kind = management.InstallationMediaURLRequest_INSTALLATION_MEDIA_KIND_DISK
		media.Platform = spec.GetCloud().GetPlatform()

		media.Format = config.TypedSpec().Value.GetDiskImageSuffix()
		if media.Format == "" {
			media.Format = diskFormatRawXZ
		}
	case spec.GetSbc() != nil:
		if format != "" {
			return media, fmt.Errorf(
				"format is not applicable to the SBC preset for overlay %q: an SBC preset always produces a raw disk image",
				spec.GetSbc().GetOverlay())
		}

		// SBC media is built for the metal platform with an overlay applied, and the factory serves no
		// secure boot variant of it.
		if spec.GetSecureBoot() {
			return media, fmt.Errorf("the SBC preset for overlay %q enables secure boot, which SBC media does not support", spec.GetSbc().GetOverlay())
		}

		media.Kind = management.InstallationMediaURLRequest_INSTALLATION_MEDIA_KIND_DISK
		media.Platform = platformMetal
		media.Format = diskFormatRawXZ
	default:
		media.Platform = platformMetal

		switch format {
		case "", installationMediaFormatISO:
			media.Kind = management.InstallationMediaURLRequest_INSTALLATION_MEDIA_KIND_ISO
		case installationMediaFormatPXE:
			media.Kind = management.InstallationMediaURLRequest_INSTALLATION_MEDIA_KIND_PXE
		default:
			// Any other value is a disk image suffix, passed to the factory as given. An alias is
			// expanded first so the shorthands omnictl offers keep working.
			suffix := format
			if alias, ok := installationMediaFormatAliases[format]; ok {
				suffix = alias
			}

			if !validDiskImageFormat(suffix) {
				return media, fmt.Errorf(
					"invalid format %q: it must be %q, %q, or a disk image suffix such as %q or %q, "+
						"containing only letters, digits, dots, underscores and dashes",
					format, installationMediaFormatISO, installationMediaFormatPXE, diskFormatRawXZ, "raw.zst")
			}

			media.Kind = management.InstallationMediaURLRequest_INSTALLATION_MEDIA_KIND_DISK
			media.Format = suffix
		}
	}

	return media, nil
}

// schematicGRPCTunnelMode maps the preset's tunnel mode onto the schematic request's enum.
func schematicGRPCTunnelMode(mode specs.GrpcTunnelMode) management.CreateSchematicRequest_SiderolinkGRPCTunnelMode {
	switch mode {
	case specs.GrpcTunnelMode_ENABLED:
		return management.CreateSchematicRequest_ENABLED
	case specs.GrpcTunnelMode_DISABLED:
		return management.CreateSchematicRequest_DISABLED
	case specs.GrpcTunnelMode_UNSET:
		fallthrough
	default:
		return management.CreateSchematicRequest_AUTO
	}
}
