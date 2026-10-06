// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/management"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                     = (*installationMediaPresetResource)(nil)
	_ resource.ResourceWithConfigure        = (*installationMediaPresetResource)(nil)
	_ resource.ResourceWithImportState      = (*installationMediaPresetResource)(nil)
	_ resource.ResourceWithConfigValidators = (*installationMediaPresetResource)(nil)
)

// errInstallationMediaPresetPlanInvalid aborts a state update when the plan cannot be converted; the
// real cause is carried in the response diagnostics.
var errInstallationMediaPresetPlanInvalid = errors.New("invalid installation media preset plan")

// Architecture names accepted by the `architecture` attribute, matching those understood by
// `omnictl media preset create --arch`.
const (
	installationMediaArchAMD64 = "amd64"
	installationMediaArchARM64 = "arm64"
)

// Bootloader names accepted by the `bootloader` attribute, matching those understood by
// `omnictl media preset create --bootloader`.
const (
	installationMediaBootloaderAuto = "auto"
	installationMediaBootloaderUEFI = "uefi"
	installationMediaBootloaderBIOS = "bios"
	installationMediaBootloaderDual = "dual"
)

// kernelArgPattern matches a single kernel argument. The arguments are stored as one
// space-separated string and read back with strings.Fields, so an element carrying whitespace would
// come back as several elements and diff forever against the configuration.
var kernelArgPattern = regexp.MustCompile(`^\S+$`)

// installationMediaArchitectures maps the user-facing architecture names onto the spec enum.
var installationMediaArchitectures = map[string]specs.PlatformConfigSpec_Arch{
	installationMediaArchAMD64: specs.PlatformConfigSpec_AMD64,
	installationMediaArchARM64: specs.PlatformConfigSpec_ARM64,
}

// installationMediaBootloaders maps the user-facing bootloader names onto the schematic enum.
//
// The names are the ones an operator recognizes rather than the enum's own: `uefi` selects
// systemd-boot and `bios` selects GRUB, the same pairing `omnictl` uses.
var installationMediaBootloaders = map[string]management.SchematicBootloader{
	installationMediaBootloaderAuto: management.SchematicBootloader_BOOT_AUTO,
	installationMediaBootloaderUEFI: management.SchematicBootloader_BOOT_SD,
	installationMediaBootloaderBIOS: management.SchematicBootloader_BOOT_GRUB,
	installationMediaBootloaderDual: management.SchematicBootloader_BOOT_DUAL,
}

// installationMediaPresetResourceModel maps the omni_installation_media_preset resource schema.
type installationMediaPresetResourceModel struct {
	Cloud                 *installationMediaPresetCloudModel `tfsdk:"cloud"`
	SBC                   *installationMediaPresetSBCModel   `tfsdk:"sbc"`
	Name                  types.String                       `tfsdk:"name"`
	Architecture          types.String                       `tfsdk:"architecture"`
	TalosVersion          types.String                       `tfsdk:"talos_version"`
	JoinToken             types.String                       `tfsdk:"join_token"`
	Bootloader            types.String                       `tfsdk:"bootloader"`
	EmbeddedMachineConfig types.String                       `tfsdk:"embedded_machine_config"`
	ImageFactoryURL       types.String                       `tfsdk:"image_factory_url"`
	Extensions            types.Set                          `tfsdk:"extensions"`
	KernelArgs            types.List                         `tfsdk:"kernel_args"`
	MachineLabels         types.Map                          `tfsdk:"machine_labels"`
	GRPCTunnel            types.Bool                         `tfsdk:"grpc_tunnel"`
	SecureBoot            types.Bool                         `tfsdk:"secure_boot"`
}

// installationMediaPresetCloudModel maps the nested `cloud` block, which turns the preset into a
// cloud image for a specific platform.
type installationMediaPresetCloudModel struct {
	Platform types.String `tfsdk:"platform"`
}

// installationMediaPresetSBCModel maps the nested `sbc` block, which turns the preset into a
// single-board-computer image built with an overlay.
type installationMediaPresetSBCModel struct {
	Overlay        types.String `tfsdk:"overlay"`
	OverlayOptions types.String `tfsdk:"overlay_options"`
}

// installationMediaPresetResource implements the omni_installation_media_preset resource.
type installationMediaPresetResource struct {
	data *providerData
}

// NewInstallationMediaPresetResource returns a new omni_installation_media_preset resource.
func NewInstallationMediaPresetResource() resource.Resource {
	return &installationMediaPresetResource{}
}

// Metadata implements resource.Resource.
func (r *installationMediaPresetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installation_media_preset"
}

// Schema implements resource.Resource.
func (r *installationMediaPresetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a saved installation media preset: a named, reusable set of image factory parameters that " +
			"`omnictl media download` builds installation media from. A preset targets exactly one of bare metal (neither " +
			"`cloud` nor `sbc`), a cloud platform (`cloud`) or a single-board computer (`sbc`).\n\n" +
			"A preset describes *what* is in the media, not which file format it is delivered as. A bare-metal preset " +
			"picks its format per download with `omnictl media download <name> --format iso|raw|qcow2|pxe`, defaulting " +
			"to an ISO, so one preset serves every boot method. A cloud preset's format follows its platform and an SBC " +
			"preset always produces a raw disk image, which is why `--format` is rejected for both.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The preset name. This is the resource ID and is immutable; changing it replaces the preset.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"architecture": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(installationMediaArchAMD64),
				Description: "The image architecture. One of: `amd64`, `arm64`.",
				Validators: []validator.String{
					stringvalidator.OneOf(installationMediaArchAMD64, installationMediaArchARM64),
				},
			},
			"talos_version": schema.StringAttribute{
				Optional: true,
				Description: "The Talos version the media is built for, in semver format (e.g. `1.13.5`). Leave unset to " +
					"track whichever version is the Omni instance's default at download time.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"extensions": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Talos system extensions to pre-install, by full catalog name (e.g. `siderolabs/qemu-guest-agent`). " +
					"Unlike `omnictl`, short names are not resolved: Omni rejects names that are not in the extensions catalog " +
					"for the preset's Talos version.",
			},
			"kernel_args": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Extra kernel arguments, one per element (e.g. `console=ttyS0`). They are stored as a single " +
					"space-separated string, so an element must not itself contain whitespace.",
				Validators: []validator.List{
					listvalidator.ValueStringsAre(
						stringvalidator.RegexMatches(kernelArgPattern, "must be a single kernel argument, with no whitespace in it"),
					),
				},
			},
			"cloud": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Builds the preset as a cloud image for a specific platform. The platform determines the image " +
					"format. Conflicts with `sbc`; omit both for bare-metal media, whose format is chosen at download time.",
				Attributes: map[string]schema.Attribute{
					"platform": schema.StringAttribute{
						Required:    true,
						Description: "The cloud platform, e.g. `aws`, `gcp`, `azure`, `vultr`. Omni rejects platforms it does not know.",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
						},
					},
				},
			},
			"sbc": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Builds the preset as a single-board-computer image using an overlay. Always produces a raw disk " +
					"image. Conflicts with `cloud`; omit both for bare-metal media, whose format is chosen at download time.",
				Attributes: map[string]schema.Attribute{
					"overlay": schema.StringAttribute{
						Required:    true,
						Description: "The SBC overlay name, e.g. `rpi_generic`, `rockpi_4c`. Omni rejects overlays it does not know.",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
						},
					},
					"overlay_options": schema.StringAttribute{
						Optional:    true,
						Description: "Overlay options, as a YAML string.",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
						},
					},
				},
			},
			"join_token": schema.StringAttribute{
				Optional: true,
				Description: "The ID of the join token machines booted from this media use. Leave unset to track whichever " +
					"token is the Omni instance's default at download time.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"secure_boot": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Build SecureBoot media.",
			},
			"grpc_tunnel": schema.BoolAttribute{
				Optional: true,
				Description: "Configure Talos to tunnel SideroLink management traffic over HTTP/2 instead of WireGuard's UDP. " +
					"Leave unset to let Omni decide at download time. Only enable this when the network blocks UDP: the " +
					"tunnel adds significant overhead.",
			},
			"machine_labels": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Labels applied to every machine that joins Omni from media built with this preset.",
			},
			"bootloader": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(installationMediaBootloaderAuto),
				Description: "The bootloader to install. One of: `auto`, `uefi` (systemd-boot), `bios` (GRUB), `dual`.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						installationMediaBootloaderAuto,
						installationMediaBootloaderUEFI,
						installationMediaBootloaderBIOS,
						installationMediaBootloaderDual,
					),
				},
			},
			"embedded_machine_config": schema.StringAttribute{
				Optional: true,
				Description: "A Talos machine configuration embedded into the media, as the configuration body itself. Pair " +
					"it with `file()` to read it from disk. Requires a Talos version that supports embedded configuration.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"image_factory_url": schema.StringAttribute{
				Computed: true,
				Description: "The image factory this preset is pinned to. Resolved by the provider from the Omni instance's " +
					"configuration: the factory that serves `talos_version`, or the instance's primary factory when the " +
					"version is unset or served by the primary.",
			},
		},
	}
}

// ConfigValidators implements resource.ResourceWithConfigValidators.
func (r *installationMediaPresetResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(
			path.MatchRoot("cloud"),
			path.MatchRoot("sbc"),
		),
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *installationMediaPresetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *installationMediaPresetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan installationMediaPresetResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	imageFactoryURL, err := resolveImageFactoryURL(ctx, r.data.state, normalizeTalosVersion(plan.TalosVersion.ValueString()))
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to resolve the Omni image factory", err)

		return
	}

	preset := omni.NewInstallationMediaConfig(plan.Name.ValueString())

	r.applyInstallationMediaPresetModel(ctx, plan, imageFactoryURL, preset, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	if err = r.data.state.Create(ctx, preset); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni installation media preset", err)

		return
	}

	plan.ImageFactoryURL = types.StringValue(imageFactoryURL)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *installationMediaPresetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state installationMediaPresetResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	preset, err := safe.ReaderGetByID[*omni.InstallationMediaConfig](ctx, r.data.state, state.Name.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni installation media preset", err)

		return
	}

	r.installationMediaPresetToModel(ctx, preset, &state, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Everything but the name is mutable in place; Omni validates
// the result the same way it validates a create.
func (r *installationMediaPresetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan installationMediaPresetResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	imageFactoryURL, err := resolveImageFactoryURL(ctx, r.data.state, normalizeTalosVersion(plan.TalosVersion.ValueString()))
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to resolve the Omni image factory", err)

		return
	}

	_, err = safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewInstallationMediaConfig(plan.Name.ValueString()).Metadata(),
		func(preset *omni.InstallationMediaConfig) error {
			r.applyInstallationMediaPresetModel(ctx, plan, imageFactoryURL, preset, &resp.Diagnostics)

			if resp.Diagnostics.HasError() {
				return errInstallationMediaPresetPlanInvalid
			}

			return nil
		})
	if err != nil {
		if resp.Diagnostics.HasError() {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to update Omni installation media preset", err)

		return
	}

	plan.ImageFactoryURL = types.StringValue(imageFactoryURL)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *installationMediaPresetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state installationMediaPresetResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	preset := omni.NewInstallationMediaConfig(state.Name.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, preset.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni installation media preset", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Presets are imported by their name.
func (r *installationMediaPresetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// applyInstallationMediaPresetModel builds an InstallationMediaConfig spec from the model,
// validating the model first.
func (r *installationMediaPresetResource) applyInstallationMediaPresetModel(
	ctx context.Context, plan installationMediaPresetResourceModel, imageFactoryURL string,
	preset *omni.InstallationMediaConfig, diags *diag.Diagnostics,
) {
	if plan.Cloud != nil && plan.SBC != nil {
		diags.AddError(
			"Invalid installation media preset",
			"cloud and sbc are mutually exclusive: a preset targets a cloud platform, an SBC overlay, or bare metal.",
		)

		return
	}

	architecture, ok := installationMediaArchitectures[plan.Architecture.ValueString()]
	if !ok {
		diags.AddAttributeError(
			path.Root("architecture"),
			"Invalid architecture",
			fmt.Sprintf("Unsupported architecture %q, must be one of: %s, %s.", plan.Architecture.ValueString(), installationMediaArchAMD64, installationMediaArchARM64),
		)

		return
	}

	bootloader, ok := installationMediaBootloaders[plan.Bootloader.ValueString()]
	if !ok {
		diags.AddAttributeError(
			path.Root("bootloader"),
			"Invalid bootloader",
			fmt.Sprintf("Unknown bootloader %q, must be one of: %s, %s, %s, %s.", plan.Bootloader.ValueString(),
				installationMediaBootloaderAuto, installationMediaBootloaderUEFI, installationMediaBootloaderBIOS, installationMediaBootloaderDual),
		)

		return
	}

	var (
		extensions  []string
		kernelArgs  []string
		labels      map[string]string
		cloudConfig *specs.InstallationMediaConfigSpec_Cloud
		sbcConfig   *specs.InstallationMediaConfigSpec_SBC
	)

	if !plan.Extensions.IsNull() {
		diags.Append(plan.Extensions.ElementsAs(ctx, &extensions, false)...)
	}

	if !plan.KernelArgs.IsNull() {
		diags.Append(plan.KernelArgs.ElementsAs(ctx, &kernelArgs, false)...)
	}

	if !plan.MachineLabels.IsNull() {
		diags.Append(plan.MachineLabels.ElementsAs(ctx, &labels, false)...)
	}

	if diags.HasError() {
		return
	}

	// Re-check what the schema validator already enforces: joining an argument that carries
	// whitespace would make it come back from the server as several arguments, so it must never reach
	// the spec, whatever path got here.
	for i, arg := range kernelArgs {
		if !kernelArgPattern.MatchString(arg) {
			diags.AddAttributeError(
				path.Root("kernel_args").AtListIndex(i),
				"Invalid kernel argument",
				fmt.Sprintf("Kernel argument %q contains whitespace. Kernel arguments are stored space-separated, so each "+
					"element must be a single argument.", arg),
			)

			return
		}
	}

	if plan.Cloud != nil {
		cloudConfig = &specs.InstallationMediaConfigSpec_Cloud{
			Platform: plan.Cloud.Platform.ValueString(),
		}
	}

	if plan.SBC != nil {
		sbcConfig = &specs.InstallationMediaConfigSpec_SBC{
			Overlay:        plan.SBC.Overlay.ValueString(),
			OverlayOptions: plan.SBC.OverlayOptions.ValueString(),
		}
	}

	value := preset.TypedSpec().Value

	// Strip a leading "v" so the stored value matches the canonical Talos version form Omni validates
	// against, and so it round-trips back into state unchanged.
	value.TalosVersion = normalizeTalosVersion(plan.TalosVersion.ValueString())
	value.Architecture = architecture
	value.InstallExtensions = extensions
	value.KernelArgs = strings.Join(kernelArgs, " ")
	value.Cloud = cloudConfig
	value.Sbc = sbcConfig
	value.JoinToken = plan.JoinToken.ValueString()
	value.SecureBoot = plan.SecureBoot.ValueBool()
	value.GrpcTunnel = grpcTunnelMode(plan.GRPCTunnel)
	value.MachineLabels = labels
	value.Bootloader = bootloader
	value.EmbeddedMachineConfig = plan.EmbeddedMachineConfig.ValueString()
	value.ImageFactoryUrl = imageFactoryURL
}

// installationMediaPresetToModel populates the model from an InstallationMediaConfig resource read
// from Omni.
func (r *installationMediaPresetResource) installationMediaPresetToModel(
	ctx context.Context, preset *omni.InstallationMediaConfig, model *installationMediaPresetResourceModel, diags *diag.Diagnostics,
) {
	value := preset.TypedSpec().Value

	model.Name = types.StringValue(preset.Metadata().ID())
	model.Architecture = types.StringValue(architectureName(value.GetArchitecture()))
	model.Bootloader = types.StringValue(bootloaderName(value.GetBootloader()))
	model.SecureBoot = types.BoolValue(value.GetSecureBoot())
	model.ImageFactoryURL = types.StringValue(value.GetImageFactoryUrl())

	// The optional attributes are not Computed: the server reports the zero value for anything the
	// user left unset, and materializing "" into state would diff forever against a null config.
	model.TalosVersion = optionalString(value.GetTalosVersion())
	model.JoinToken = optionalString(value.GetJoinToken())
	model.EmbeddedMachineConfig = optionalString(value.GetEmbeddedMachineConfig())

	switch value.GetGrpcTunnel() {
	case specs.GrpcTunnelMode_ENABLED:
		model.GRPCTunnel = types.BoolValue(true)
	case specs.GrpcTunnelMode_DISABLED:
		model.GRPCTunnel = types.BoolValue(false)
	case specs.GrpcTunnelMode_UNSET:
		fallthrough
	default:
		model.GRPCTunnel = types.BoolNull()
	}

	if cloud := value.GetCloud(); cloud != nil {
		model.Cloud = &installationMediaPresetCloudModel{
			Platform: types.StringValue(cloud.GetPlatform()),
		}
	} else {
		model.Cloud = nil
	}

	if sbc := value.GetSbc(); sbc != nil {
		model.SBC = &installationMediaPresetSBCModel{
			Overlay:        types.StringValue(sbc.GetOverlay()),
			OverlayOptions: optionalString(sbc.GetOverlayOptions()),
		}
	} else {
		model.SBC = nil
	}

	// The collection attributes are Optional too, so an empty server-side value must map back to null
	// rather than to an empty collection.
	model.Extensions = optionalSet(ctx, value.GetInstallExtensions(), diags)
	model.KernelArgs = optionalList(ctx, strings.Fields(value.GetKernelArgs()), diags)
	model.MachineLabels = optionalMap(ctx, value.GetMachineLabels(), diags)
}

// normalizeTalosVersion strips the leading "v" Omni does not store, so that `v1.13.5` and `1.13.5`
// are the same version.
func normalizeTalosVersion(version string) string {
	return strings.TrimPrefix(version, "v")
}

// normalizeImageFactoryURL strips a trailing slash, matching how Omni canonicalizes factory URLs
// before comparing them.
func normalizeImageFactoryURL(url string) string {
	return strings.TrimRight(url, "/")
}

// grpcTunnelMode maps the tri-state `grpc_tunnel` attribute onto the spec enum: an unset attribute
// leaves the decision to Omni at download time.
func grpcTunnelMode(enabled types.Bool) specs.GrpcTunnelMode {
	switch {
	case enabled.IsNull() || enabled.IsUnknown():
		return specs.GrpcTunnelMode_UNSET
	case enabled.ValueBool():
		return specs.GrpcTunnelMode_ENABLED
	default:
		return specs.GrpcTunnelMode_DISABLED
	}
}

// architectureName is the inverse of the installationMediaArchitectures lookup.
func architectureName(arch specs.PlatformConfigSpec_Arch) string {
	switch arch {
	case specs.PlatformConfigSpec_ARM64:
		return installationMediaArchARM64
	case specs.PlatformConfigSpec_AMD64:
		fallthrough
	case specs.PlatformConfigSpec_UNKNOWN_ARCH:
		fallthrough
	default:
		return installationMediaArchAMD64
	}
}

// bootloaderName is the inverse of the installationMediaBootloaders lookup.
func bootloaderName(bootloader management.SchematicBootloader) string {
	switch bootloader {
	case management.SchematicBootloader_BOOT_SD:
		return installationMediaBootloaderUEFI
	case management.SchematicBootloader_BOOT_GRUB:
		return installationMediaBootloaderBIOS
	case management.SchematicBootloader_BOOT_DUAL:
		return installationMediaBootloaderDual
	case management.SchematicBootloader_BOOT_AUTO:
		fallthrough
	default:
		return installationMediaBootloaderAuto
	}
}
