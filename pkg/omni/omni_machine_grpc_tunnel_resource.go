// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*machineGRPCTunnelResource)(nil)
	_ resource.ResourceWithConfigure   = (*machineGRPCTunnelResource)(nil)
	_ resource.ResourceWithImportState = (*machineGRPCTunnelResource)(nil)
)

// machineGRPCTunnelResourceModel maps the omni_machine_grpc_tunnel resource schema.
type machineGRPCTunnelResourceModel struct {
	MachineID types.String `tfsdk:"machine_id"`
	Enabled   types.Bool   `tfsdk:"enabled"`
}

// machineGRPCTunnelResource implements the omni_machine_grpc_tunnel resource.
type machineGRPCTunnelResource struct {
	data *providerData
}

// NewMachineGRPCTunnelResource returns a new omni_machine_grpc_tunnel resource.
func NewMachineGRPCTunnelResource() resource.Resource {
	return &machineGRPCTunnelResource{}
}

// Metadata implements resource.Resource.
func (r *machineGRPCTunnelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_grpc_tunnel"
}

// Schema implements resource.Resource.
func (r *machineGRPCTunnelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the SideroLink connection mode of a single machine: whether its WireGuard traffic to Omni goes " +
			"over plain UDP or is tunneled through gRPC (HTTP/2), which gets through networks that block or mangle UDP. " +
			"Without this resource the machine uses the mode its installation media or kernel arguments ask for; " +
			"destroying it hands the choice back to the machine.\n\n" +
			"Changing the mode of a connected machine drops its SideroLink peer, so the machine reconnects in the new " +
			"mode only after its connection times out, which takes about five minutes. Omni accepts the setting " +
			"before the machine has joined, in which case the machine connects in that mode straight away.",
		Attributes: map[string]schema.Attribute{
			"machine_id": schema.StringAttribute{
				Required:    true,
				Description: "The machine UUID the setting applies to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether the machine tunnels its SideroLink traffic through gRPC (`true`) or uses plain UDP (`false`).",
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineGRPCTunnelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *machineGRPCTunnelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineGRPCTunnelResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	config := siderolink.NewGRPCTunnelConfig(plan.MachineID.ValueString())
	config.TypedSpec().Value.Enabled = plan.Enabled.ValueBool()

	if err := r.data.state.Create(ctx, config); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni machine gRPC tunnel configuration", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *machineGRPCTunnelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineGRPCTunnelResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	config, err := safe.ReaderGetByID[*siderolink.GRPCTunnelConfig](ctx, r.data.state, state.MachineID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni machine gRPC tunnel configuration", err)

		return
	}

	state.MachineID = types.StringValue(config.Metadata().ID())
	state.Enabled = types.BoolValue(config.TypedSpec().Value.GetEnabled())

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. The machine forces replacement; the mode is updated in place.
func (r *machineGRPCTunnelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan machineGRPCTunnelResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	updateWithDiags(ctx, r.data.state, siderolink.NewGRPCTunnelConfig(plan.MachineID.ValueString()).Metadata(), &resp.Diagnostics,
		"Failed to update Omni machine gRPC tunnel configuration",
		func(config *siderolink.GRPCTunnelConfig) {
			config.TypedSpec().Value.Enabled = plan.Enabled.ValueBool()
		})

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource. Without the configuration the machine picks the mode itself
// again, which `omnictl configure machine --siderolink-connection auto` does the same way.
func (r *machineGRPCTunnelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineGRPCTunnelResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	config := siderolink.NewGRPCTunnelConfig(state.MachineID.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, config.Metadata()); err != nil && !cosistate.IsNotFoundError(err) {
		errToDiag(&resp.Diagnostics, "Failed to destroy Omni machine gRPC tunnel configuration", err)
	}
}

// ImportState implements resource.ResourceWithImportState. The configuration is imported by the
// machine UUID.
func (r *machineGRPCTunnelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("machine_id"), req, resp)
}
