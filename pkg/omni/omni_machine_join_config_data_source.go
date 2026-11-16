// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/client/management"
	"github.com/siderolabs/omni/client/pkg/jointoken"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
)

// Ensure the data source satisfies the framework interfaces.
var (
	_ datasource.DataSource                     = (*machineJoinConfigDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*machineJoinConfigDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*machineJoinConfigDataSource)(nil)
)

// machineJoinConfigDataSourceModel maps the omni_machine_join_config data source schema.
type machineJoinConfigDataSourceModel struct {
	KernelArgs    types.List   `tfsdk:"kernel_args"`
	MachineLabels types.Map    `tfsdk:"machine_labels"`
	TokenID       types.String `tfsdk:"token_id"`
	TokenName     types.String `tfsdk:"token_name"`
	JoinToken     types.String `tfsdk:"join_token"`
	Config        types.String `tfsdk:"config"`
	KernelArgsCmd types.String `tfsdk:"kernel_args_cmdline"`
	OmniEndpoint  types.String `tfsdk:"omni_endpoint"`
	UseGRPCTunnel types.Bool   `tfsdk:"use_grpc_tunnel"`
}

// machineJoinConfigDataSource implements the omni_machine_join_config data source.
type machineJoinConfigDataSource struct {
	data *providerData
}

// NewMachineJoinConfigDataSource returns a new omni_machine_join_config data source.
func NewMachineJoinConfigDataSource() datasource.DataSource {
	return &machineJoinConfigDataSource{}
}

// Metadata implements datasource.DataSource.
func (d *machineJoinConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_join_config"
}

// Schema implements datasource.DataSource.
func (d *machineJoinConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Returns what a machine needs in order to register itself with this Omni instance: a partial Talos " +
			"machine configuration and the equivalent kernel arguments, both carrying a join token.\n\n" +
			"It is how a machine booted from a stock Talos image joins Omni without Omni-specific installation media. " +
			"Pass `config` as the machine configuration (user data on a cloud, an `--config` document on bare metal), " +
			"or `kernel_args` on the kernel command line when booting over PXE or when the platform takes a command " +
			"line rather than a config. The two are alternatives, not a pair.\n\n" +
			"It is the Terraform equivalent of `omnictl jointoken machine-config` and `omnictl jointoken kernel-args`.\n\n" +
			"Everything returned by this data source embeds the join token, so every output is marked sensitive. Writing any of it " +
			"somewhere readable, a boot script or a CI log, hands the ability to join this instance to whoever can " +
			"read it.",
		Attributes: map[string]schema.Attribute{
			"token_id": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "The join token to embed, i.e. the `id` of an `omni_join_token`. Mutually exclusive with " +
					"`token_name`; with neither, the instance default is used, which `omni_default_join_token` sets.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"token_name": schema.StringAttribute{
				Optional: true,
				Description: "Looks the join token up by its `name` instead of its value, which keeps the token out of " +
					"the configuration. Use `initial` for the token Omni bootstrapped itself with, which has no name of " +
					"its own.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"use_grpc_tunnel": schema.BoolAttribute{
				Optional: true,
				Description: "Tunnel the SideroLink WireGuard traffic over gRPC instead of sending it as UDP. Needed " +
					"where the machine cannot reach Omni's SideroLink UDP port, for instance behind a proxy or a " +
					"firewall that passes only HTTPS. It costs throughput, so leave it off where plain UDP works.",
			},
			"machine_labels": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Labels to put on every machine that joins with this configuration. They are signed into " +
					"the join token, so a machine cannot alter them, and Omni copies them onto the machine when it first " +
					"registers. From then on they are ordinary user labels that can be edited or removed in Omni. " +
					"A label unique to one machine is how to find that machine once it has joined, see " +
					"`omni_machine_wait`. Keys under the `omni.sidero.dev/` prefix are reserved. The labels travel on the " +
					"kernel command line, so the encoded token, labels included, is limited to 1024 bytes.",
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"join_token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The join token that was actually used, resolved from `token_id`, `token_name` or the " +
					"instance default.",
			},
			"config": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The partial Talos machine configuration that joins the machine to Omni, as YAML. It is a " +
					"fragment rather than a whole configuration: Omni sends the rest once the machine has registered.",
			},
			"kernel_args": schema.ListAttribute{
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "The same thing as kernel arguments, for booting a machine that takes a command line rather " +
					"than a configuration. A list, in the order Omni returns them; `kernel_args_cmdline` is the same " +
					"values already joined.",
			},
			"kernel_args_cmdline": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "`kernel_args` joined with spaces, ready to append to a kernel command line in an iPXE " +
					"script or a hypervisor's boot arguments field.",
			},
			"omni_endpoint": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "Omni's machine API URL with the join token attached as a query parameter, which is what " +
					"the `siderolink.api` kernel argument carries, including the gRPC tunnel setting when `use_grpc_tunnel` " +
					"is set. Useful where something needs the join URL on its own " +
					"rather than a whole command line. Equivalent to `omnictl jointoken omni-endpoint`.",
			},
		},
	}
}

// ConfigValidators implements datasource.DataSourceWithConfigValidators.
func (d *machineJoinConfigDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		// A token named one way and valued another cannot be reconciled, so refuse both rather than
		// silently preferring one, which is what omnictl's mutually exclusive flags do.
		datasourcevalidator.Conflicting(
			path.MatchRoot("token_id"),
			path.MatchRoot("token_name"),
		),
	}
}

// Configure implements datasource.DataSourceWithConfigure.
func (d *machineJoinConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Read implements datasource.DataSource.
func (d *machineJoinConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config machineJoinConfigDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	token, err := resolveJoinToken(ctx, d.data.state, config.TokenID.ValueString(), config.TokenName.ValueString())
	if err != nil {
		switch {
		case errors.Is(err, errJoinTokenNameNotFound):
			resp.Diagnostics.AddAttributeError(
				path.Root("token_name"),
				"Omni join token not found",
				fmt.Sprintf("No join token named %q exists.", config.TokenName.ValueString()),
			)
		case cosistate.IsNotFoundError(err) && !config.TokenID.IsNull():
			resp.Diagnostics.AddAttributeError(
				path.Root("token_id"),
				"Omni join token not found",
				"The given join token does not exist on this Omni instance.",
			)
		default:
			errToDiag(&resp.Diagnostics, "Failed to resolve the Omni join token", err)
		}

		return
	}

	var opts []management.MachineJoinConfigOption

	if !config.MachineLabels.IsNull() && !config.MachineLabels.IsUnknown() {
		labels := map[string]string{}

		resp.Diagnostics.Append(config.MachineLabels.ElementsAs(ctx, &labels, false)...)

		if resp.Diagnostics.HasError() {
			return
		}

		// Checking here turns what Omni would refuse into an error on the attribute itself.
		if err = jointoken.ValidateLabels(labels); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("machine_labels"), "Invalid machine labels", err.Error())

			return
		}

		opts = append(opts, management.WithMachineLabels(labels))
	}

	joinConfig, err := d.data.client.Management().GetMachineJoinConfig(ctx, token, config.UseGRPCTunnel.ValueBool(), opts...)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to get the Omni machine join configuration", err)

		return
	}

	// Omni releases without label support ignore the field they do not know and hand back a token
	// without the labels, so the machines would join unlabelled, and anything waiting on a label
	// would wait for nothing. Catch that here instead.
	if len(opts) > 0 {
		if err = checkJoinTokenCarriesLabels(joinConfig.GetKernelArgs()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("machine_labels"), "Machine labels are not supported by this Omni instance", err.Error())

			return
		}
	}

	apiConfig, err := safe.ReaderGetByID[*siderolink.APIConfig](ctx, d.data.state, siderolink.ConfigID)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read the Omni SideroLink API configuration", err)

		return
	}

	kernelArgs, diags := types.ListValueFrom(ctx, types.StringType, joinConfig.GetKernelArgs())
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	config.JoinToken = types.StringValue(token)
	config.Config = types.StringValue(joinConfig.GetConfig())
	config.KernelArgs = kernelArgs
	config.KernelArgsCmd = types.StringValue(strings.Join(joinConfig.GetKernelArgs(), " "))
	config.OmniEndpoint = types.StringValue(joinEndpoint(joinConfig.GetKernelArgs(), apiConfig.TypedSpec().Value.GetMachineApiAdvertisedUrl(), token))

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// siderolinkAPIKernelArg is the kernel argument that carries the join URL.
const siderolinkAPIKernelArg = "siderolink.api="

// joinEndpoint returns the join URL machines are given.
//
// Omni builds it into the `siderolink.api` kernel argument, along with whatever else the request
// asked for, such as the gRPC tunnel, so it is taken from there to match exactly. Only when that
// argument is missing is it rebuilt from the advertised machine API URL, as `omnictl jointoken
// omni-endpoint` does.
func joinEndpoint(kernelArgs []string, baseURL, token string) string {
	for _, arg := range kernelArgs {
		if endpoint, ok := strings.CutPrefix(arg, siderolinkAPIKernelArg); ok && endpoint != "" {
			return endpoint
		}
	}

	return joinURL(baseURL, token)
}

// joinURL attaches a join token to Omni's machine API URL as a query parameter, matching what Omni
// itself puts in the `siderolink.api` kernel argument.
func joinURL(baseURL, token string) string {
	separator := "?"

	if strings.Contains(baseURL, "?") {
		separator = "&"
	}

	return baseURL + separator + "jointoken=" + token
}

// checkJoinTokenCarriesLabels verifies that the join token in the kernel arguments Omni returned is
// one that can carry labels.
func checkJoinTokenCarriesLabels(kernelArgs []string) error {
	for _, arg := range kernelArgs {
		value, ok := strings.CutPrefix(arg, "siderolink.api=")
		if !ok {
			continue
		}

		apiURL, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("failed to parse the SideroLink API URL Omni returned: %w", err)
		}

		token, err := jointoken.Parse(apiURL.Query().Get("jointoken"))
		if err != nil {
			return fmt.Errorf("failed to parse the join token Omni returned: %w", err)
		}

		if token.Version != jointoken.Version3 {
			return errors.New("the join token Omni returned does not carry the labels, which means this Omni predates " +
				"machine labels in join tokens: upgrade it to v1.13 or later, or leave machine_labels unset")
		}

		return nil
	}

	return errors.New("the kernel arguments Omni returned have no SideroLink API URL to check the join token in")
}
