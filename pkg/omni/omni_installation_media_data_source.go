// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"net/url"
	gopath "path"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/management"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// installationMediaURLMinOmniVersion is the first Omni release serving GetInstallationMediaURL.
const installationMediaURLMinOmniVersion = "v1.11.0"

// Ensure the data source satisfies the framework interfaces.
var (
	_ datasource.DataSource              = (*installationMediaDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*installationMediaDataSource)(nil)
)

// installationMediaDataSourceModel maps the omni_installation_media data source schema.
type installationMediaDataSourceModel struct {
	Preset           types.String `tfsdk:"preset"`
	Format           types.String `tfsdk:"format"`
	DownloadTokenTTL types.String `tfsdk:"download_token_ttl"`
	URL              types.String `tfsdk:"url"`
	SchematicID      types.String `tfsdk:"schematic_id"`
	TalosVersion     types.String `tfsdk:"talos_version"`
	Filename         types.String `tfsdk:"filename"`
	Platform         types.String `tfsdk:"platform"`
	Architecture     types.String `tfsdk:"architecture"`
	StorageKey       types.String `tfsdk:"storage_key"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
	ImageFactoryHost types.String `tfsdk:"image_factory_host"`
	Headers          types.Map    `tfsdk:"headers"`
}

// installationMediaDataSource implements the omni_installation_media data source.
type installationMediaDataSource struct {
	data *providerData
}

// NewInstallationMediaDataSource returns a new omni_installation_media data source.
func NewInstallationMediaDataSource() datasource.DataSource {
	return &installationMediaDataSource{}
}

// Metadata implements datasource.DataSource.
func (d *installationMediaDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installation_media"
}

// Schema implements datasource.DataSource.
func (d *installationMediaDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Resolves an `omni_installation_media_preset` into a downloadable URL, so the media can be handed to " +
			"something that boots machines: a hypervisor's download API, a PXE server, or a plain fetch.\n\n" +
			"Reading this registers the preset's schematic with the image factory and asks Omni for the URL. Omni " +
			"assembles the factory URL and filename itself, so neither is composed here.\n\n" +
			"On an Omni instance with a **secondary image factory**, the preset must pin `talos_version`. Omni picks the " +
			"factory from the Talos version when registering the schematic and again when building the URL, and an " +
			"unpinned preset lets those two resolve independently; this data source refuses rather than return a URL the " +
			"factory cannot serve.\n\n" +
			"**The URL is not stable.** When the image factory requires authentication, Omni mints a short-lived " +
			"download token and puts it in the URL, so every read produces a different `url`. Key anything that caches " +
			"the download on `storage_key`, which changes only when the medium itself does. Against an unauthenticated " +
			"factory no token is issued and the URL is stable.",
		Attributes: map[string]schema.Attribute{
			"preset": schema.StringAttribute{
				Required:    true,
				Description: "The name of the `omni_installation_media_preset` to resolve.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"format": schema.StringAttribute{
				Optional: true,
				Description: "The download format, for a **bare-metal preset only**. `iso` (the default) or `pxe` for an " +
					"iPXE script URL; anything else is a disk image suffix the image factory compresses to on demand, such " +
					"as `raw.zst`, `raw.xz`, `qcow2` or `qcow2.gz`. `raw` and `qcow2` are kept as shorthands for `raw.xz` " +
					"and `qcow2`.\n\n" +
					"Prefer `raw.zst` over `raw.xz` for a disk image: it is what Talos itself defaults to from 1.8 onward, " +
					"and the only compressed disk format `proxmox_virtual_environment_download_file` can decompress.\n\n" +
					"It is rejected for a cloud preset, whose format follows its platform, and for an SBC preset, which is " +
					"always a raw disk image.",
			},
			"download_token_ttl": schema.StringAttribute{
				Optional: true,
				Description: "How long the URL needs to keep working, as a Go duration (e.g. `1h`), for a factory that " +
					"authenticates downloads with a token. Size it by when the *consumer* fetches, not when Terraform " +
					"applies: a hypervisor that queues the download may start it long afterwards. Omni's default suits a " +
					"fetch performed right away. Ignored by a factory that needs no token.",
			},
			"url": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The URL the medium is fetched from. Sensitive because it can carry a download token; anything " +
					"it is written into, such as an iPXE script or a log, exposes that token to whoever can read it.",
			},
			"schematic_id": schema.StringAttribute{
				Computed: true,
				Description: "The image factory schematic ID for the preset. Stable for a given preset, since the factory " +
					"deduplicates schematics by content.",
			},
			"talos_version": schema.StringAttribute{
				Computed: true,
				Description: "The Talos version the preset pins. Null when the preset tracks the Omni instance's default " +
					"version, which the server resolves when building the URL and does not report back.",
			},
			"filename": schema.StringAttribute{
				Computed:    true,
				Description: "The filename the image factory serves the medium under, taken from the resolved URL.",
			},
			"storage_key": schema.StringAttribute{
				Computed: true,
				Description: "An opaque identifier for the medium the URL points at. **Use this, not `url`, to key a cached " +
					"download or name a stored file**: it changes only when the medium itself changes, where the URL also " +
					"changes whenever a download token is minted.",
			},
			"expires_at": schema.StringAttribute{
				Computed: true,
				Description: "When the URL stops working, in RFC 3339 format. Null when it does not expire. It is Omni's " +
					"estimate, so treat it as the earliest moment the URL may stop working.",
			},
			"platform": schema.StringAttribute{
				Computed: true,
				Description: "The Talos platform the medium is built for, e.g. `metal` or `aws`. Taken from the preset: a " +
					"cloud preset uses its platform, and bare-metal and SBC presets both use `metal`.",
			},
			"architecture": schema.StringAttribute{
				Computed:    true,
				Description: "The architecture the medium is built for, `amd64` or `arm64`, taken from the preset.",
			},
			"image_factory_host": schema.StringAttribute{
				Computed:    true,
				Description: "The host serving the medium. Informative: fetching needs only `url` and `headers`.",
			},
			"headers": schema.MapAttribute{
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Headers to send with the fetch. Normally empty, since Omni authenticates downloads with a " +
					"token in the URL, but send them whenever they are not.",
			},
		},
	}
}

// Configure implements datasource.DataSourceWithConfigure.
func (d *installationMediaDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Read implements datasource.DataSource.
func (d *installationMediaDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config installationMediaDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	presetName := config.Preset.ValueString()

	preset, err := safe.ReaderGetByID[*omni.InstallationMediaConfig](ctx, d.data.state, presetName)
	if err != nil {
		errToDiag(&resp.Diagnostics, fmt.Sprintf("Failed to read Omni installation media preset %q", presetName), err)

		return
	}

	spec := preset.TypedSpec().Value

	// The format depends on the preset's shape, so it cannot be checked by a schema validator.
	media, err := mediaTargetFromPreset(ctx, d.data.state, spec, config.Format.ValueString())
	if err != nil {
		errToDiag(&resp.Diagnostics, "Invalid installation media request", err)

		return
	}

	var ttl *durationpb.Duration

	if !config.DownloadTokenTTL.IsNull() {
		parsed, parseErr := time.ParseDuration(config.DownloadTokenTTL.ValueString())
		if parseErr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("download_token_ttl"), "Invalid download token TTL",
				fmt.Sprintf("%q is not a valid duration: %s", config.DownloadTokenTTL.ValueString(), parseErr))

			return
		}

		if parsed <= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("download_token_ttl"), "Invalid download token TTL",
				fmt.Sprintf("download_token_ttl must be positive, got %q", config.DownloadTokenTTL.ValueString()))

			return
		}

		ttl = durationpb.New(parsed)
	}

	// CreateSchematic resolves an empty Talos version to the primary factory, while the URL lookup
	// substitutes Omni's default version and uses whichever factory serves that. With a second factory
	// configured those can be different factories, and the URL would point at one that never saw the
	// schematic. Refuse rather than hand back a URL that cannot be fetched.
	if spec.GetTalosVersion() == "" {
		secondary, secondaryErr := secondaryImageFactoryConfigured(ctx, d.data.state)
		if secondaryErr != nil {
			errToDiag(&resp.Diagnostics, "Failed to read the Omni image factory configuration", secondaryErr)

			return
		}

		if secondary {
			resp.Diagnostics.AddError(
				"The preset must pin a Talos version on an instance with a secondary image factory",
				fmt.Sprintf("Preset %q tracks the Omni instance's default Talos version, and this instance has a secondary "+
					"image factory configured. Registering the schematic and resolving the URL would then pick the factory "+
					"independently and could land on different ones, producing a URL that cannot be fetched.\n\n"+
					"Set `talos_version` on the preset so both resolve the same factory.", presetName),
			)

			return
		}
	}

	schematicRequest, err := schematicRequestFromPreset(ctx, d.data.state, spec)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to build the schematic for the installation media preset", err)

		return
	}

	schematic, err := d.data.client.Management().CreateSchematic(ctx, schematicRequest)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create the image factory schematic", err)

		return
	}

	// The Talos version is sent exactly as the preset holds it, empty included: Omni substitutes its
	// own default, which is a better answer than any version this provider could pick.
	mediaURL, err := d.data.client.Management().GetInstallationMediaURL(ctx, &management.InstallationMediaURLRequest{
		TalosVersion:          spec.GetTalosVersion(),
		SchematicId:           schematic.GetSchematicId(),
		InstallationMediaKind: media.Kind,
		Platform:              media.Platform,
		Architecture:          media.Architecture,
		Format:                media.Format,
		SecureBoot:            media.SecureBoot,
		DownloadTokenTtl:      ttl,
	})
	if err != nil {
		// An Omni that predates the installation media URL API answers Unimplemented. That reads as an
		// internal fault rather than "upgrade Omni", so say which it is.
		if status.Code(err) == codes.Unimplemented {
			resp.Diagnostics.AddError(
				"This Omni instance does not support installation media URLs",
				fmt.Sprintf("The Omni instance does not serve GetInstallationMediaURL, which this data source needs to "+
					"resolve a preset into a URL. It requires Omni %s or newer. Until then, download media with "+
					"\"omnictl media download\".\n\n%s", installationMediaURLMinOmniVersion, err),
			)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to resolve the installation media URL", err)

		return
	}

	headers, diags := types.MapValueFrom(ctx, types.StringType, mediaURL.GetHeaders())
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	config.URL = types.StringValue(mediaURL.GetUrl())
	config.SchematicID = types.StringValue(schematic.GetSchematicId())
	config.TalosVersion = optionalString(spec.GetTalosVersion())
	config.Filename = optionalString(filenameFromMediaURL(mediaURL.GetUrl()))
	config.Platform = types.StringValue(media.Platform)
	config.Architecture = types.StringValue(media.Architecture)
	config.StorageKey = optionalString(mediaURL.GetStorageKey())
	config.ImageFactoryHost = optionalString(mediaURL.GetImageFactoryHost())
	config.Headers = headers

	if expiresAt := mediaURL.GetExpiresAt(); expiresAt != nil {
		config.ExpiresAt = types.StringValue(expiresAt.AsTime().UTC().Format(time.RFC3339))
	} else {
		config.ExpiresAt = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// filenameFromMediaURL returns the filename the factory serves the medium under.
//
// It is read back out of the URL rather than composed: Omni assembles the factory's naming
// convention, and taking the last path segment keeps the two from ever disagreeing. A download token
// lives in the query string, so it does not reach the path.
func filenameFromMediaURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return gopath.Base(parsed.Path)
}
