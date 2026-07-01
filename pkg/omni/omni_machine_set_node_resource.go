// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*machineSetNodeResource)(nil)
	_ resource.ResourceWithConfigure   = (*machineSetNodeResource)(nil)
	_ resource.ResourceWithImportState = (*machineSetNodeResource)(nil)
)

// machineSetNodeResourceModel maps the omni_machine_set_node resource schema.
type machineSetNodeResourceModel struct {
	MachineID  types.String `tfsdk:"machine_id"`
	MachineSet types.String `tfsdk:"machine_set"`
	Cluster    types.String `tfsdk:"cluster"`
}

// machineSetNodeResource implements the omni_machine_set_node resource.
type machineSetNodeResource struct {
	data *providerData
}

// NewMachineSetNodeResource returns a new omni_machine_set_node resource.
func NewMachineSetNodeResource() resource.Resource {
	return &machineSetNodeResource{}
}

// Metadata implements resource.Resource.
func (r *machineSetNodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_set_node"
}

// Schema implements resource.Resource.
func (r *machineSetNodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Assigns a machine to an Omni machine set. Use this to place specific machines into a control plane " +
			"or worker machine set. Mutually exclusive with the machine set's machine_class auto-allocation.",
		Attributes: map[string]schema.Attribute{
			"machine_id": schema.StringAttribute{
				Required:    true,
				Description: "The UUID of the machine to assign. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"machine_set": schema.StringAttribute{
				Required:    true,
				Description: "The ID of the machine set to assign the machine to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cluster": schema.StringAttribute{
				Required: true,
				Description: "The ID of the cluster the machine set belongs to. Must match the machine set's cluster. " +
					"Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineSetNodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *machineSetNodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineSetNodeResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// The owner machine set is required to construct the node: its cluster/role labels are copied
	// onto the node.
	machineSet, err := safe.ReaderGetByID[*omni.MachineSet](ctx, r.data.state, plan.MachineSet.ValueString())
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read owning Omni machine set", err)

		return
	}

	// The cluster is stated explicitly for clarity; verify it agrees with the machine set so a
	// mismatched (or cluster-less) configuration fails fast instead of silently assigning the machine
	// elsewhere.
	cluster, ok := machineSet.Metadata().Labels().Get(omni.LabelCluster)
	if !ok {
		resp.Diagnostics.AddError(
			"Machine set has no cluster",
			fmt.Sprintf("The machine set %q is not associated with a cluster.", plan.MachineSet.ValueString()),
		)

		return
	}

	if cluster != plan.Cluster.ValueString() {
		resp.Diagnostics.AddError(
			"Cluster does not match machine set",
			fmt.Sprintf("The machine set %q belongs to cluster %q, but %q was configured.",
				plan.MachineSet.ValueString(), cluster, plan.Cluster.ValueString()),
		)

		return
	}

	node := omni.NewMachineSetNode(plan.MachineID.ValueString(), machineSet)

	if err = r.data.state.Create(ctx, node); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni machine set node", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *machineSetNodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineSetNodeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	node, err := safe.ReaderGetByID[*omni.MachineSetNode](ctx, r.data.state, state.MachineID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni machine set node", err)

		return
	}

	state.MachineID = types.StringValue(node.Metadata().ID())

	if machineSet, ok := node.Metadata().Labels().Get(omni.LabelMachineSet); ok {
		state.MachineSet = types.StringValue(machineSet)
	}

	if cluster, ok := node.Metadata().Labels().Get(omni.LabelCluster); ok {
		state.Cluster = types.StringValue(cluster)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Both attributes force replacement, so this is never called
// with meaningful changes; it simply persists the plan.
func (r *machineSetNodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan machineSetNodeResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *machineSetNodeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineSetNodeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	node := omni.NewMachineSetNode(state.MachineID.ValueString(), omni.NewMachineSet(state.MachineSet.ValueString()))

	if err := r.data.state.TeardownAndDestroy(ctx, node.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni machine set node", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Nodes are imported by machine UUID.
func (r *machineSetNodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("machine_id"), req, resp)
}
