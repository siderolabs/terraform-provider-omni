// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/gen/pair"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                     = (*machineExtensionsResource)(nil)
	_ resource.ResourceWithConfigure        = (*machineExtensionsResource)(nil)
	_ resource.ResourceWithImportState      = (*machineExtensionsResource)(nil)
	_ resource.ResourceWithConfigValidators = (*machineExtensionsResource)(nil)
)

// errExtensionsPlanInvalid aborts a state update when the plan cannot be converted; the real cause
// is carried in the response diagnostics.
var errExtensionsPlanInvalid = errors.New("invalid extensions plan")

// machineExtensionsResourceModel maps the omni_machine_extensions resource schema.
type machineExtensionsResourceModel struct {
	ID         types.String                    `tfsdk:"id"`
	Cluster    types.String                    `tfsdk:"cluster"`
	Selector   *machineExtensionsSelectorModel `tfsdk:"selector"`
	Extensions types.Set                       `tfsdk:"extensions"`
}

// machineExtensionsSelectorModel maps the nested `selector` block: it narrows a cluster-scoped
// extensions configuration to a single machine set or cluster machine. At most one field may be set.
type machineExtensionsSelectorModel struct {
	MachineSet     types.String `tfsdk:"machine_set"`
	ClusterMachine types.String `tfsdk:"cluster_machine"`
}

// machineExtensionsResource implements the omni_machine_extensions resource.
type machineExtensionsResource struct {
	data *providerData
}

// NewMachineExtensionsResource returns a new omni_machine_extensions resource.
func NewMachineExtensionsResource() resource.Resource {
	return &machineExtensionsResource{}
}

// Metadata implements resource.Resource.
func (r *machineExtensionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_extensions"
}

// Schema implements resource.Resource.
func (r *machineExtensionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the set of Talos system extensions installed on a cluster, machine set or single cluster " +
			"machine. The extensions apply to the whole `cluster` by default; a `selector` block narrows them to a single " +
			"machine set or cluster machine. Extensions defined at a narrower scope override those of a broader one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The generated extensions configuration ID, composed as `schematic-<target>`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster": schema.StringAttribute{
				Required:    true,
				Description: "The cluster the extensions apply to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"selector": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Narrows the extensions to a single machine set or cluster machine of the `cluster`. At most " +
					"one of its fields may be set. Immutable.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"machine_set": schema.StringAttribute{
						Optional:    true,
						Description: "Restrict the extensions to a single machine set of the cluster (by machine set ID).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"cluster_machine": schema.StringAttribute{
						Optional:    true,
						Description: "Restrict the extensions to a single cluster machine (by machine UUID).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},
			"extensions": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "The set of Talos system extensions to install, by name (e.g. `siderolabs/iscsi-tools`). " +
					"An empty set clears all extensions for the target.",
			},
		},
	}
}

// ConfigValidators implements resource.ResourceWithConfigValidators.
func (r *machineExtensionsResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(
			path.MatchRoot("selector").AtName("machine_set"),
			path.MatchRoot("selector").AtName("cluster_machine"),
		),
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineExtensionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// machineExtensionsID composes the resource ID from the target suffix, matching the convention used
// by Omni's own cluster templates (`schematic-<target>`).
func machineExtensionsID(suffix string) string {
	return "schematic-" + suffix
}

// targetSuffix returns the most specific scope identifier configured on the model. The extensions
// configuration is keyed by its narrowest target so distinct scopes never collide.
func (r *machineExtensionsResource) targetSuffix(plan machineExtensionsResourceModel) string {
	if plan.Selector != nil {
		if v := plan.Selector.ClusterMachine.ValueString(); v != "" {
			return v
		}

		if v := plan.Selector.MachineSet.ValueString(); v != "" {
			return v
		}
	}

	return plan.Cluster.ValueString()
}

// scopeLabels returns the scope labels derived from the model. The cluster label is always set; a
// machine set or cluster machine label narrows the scope further.
func (r *machineExtensionsResource) scopeLabels(plan machineExtensionsResourceModel) []pair.Pair[string, string] {
	labels := []pair.Pair[string, string]{
		pair.MakePair(omni.LabelCluster, plan.Cluster.ValueString()),
	}

	if plan.Selector != nil {
		if v := plan.Selector.MachineSet.ValueString(); v != "" {
			labels = append(labels, pair.MakePair(omni.LabelMachineSet, v))
		}

		if v := plan.Selector.ClusterMachine.ValueString(); v != "" {
			labels = append(labels, pair.MakePair(omni.LabelClusterMachine, v))
		}
	}

	return labels
}

// applyExtensions copies the extensions list from the model onto the configuration spec.
func (r *machineExtensionsResource) applyExtensions(ctx context.Context, plan machineExtensionsResourceModel, config *omni.ExtensionsConfiguration, diags *diag.Diagnostics) {
	var extensions []string

	diags.Append(plan.Extensions.ElementsAs(ctx, &extensions, false)...)

	config.TypedSpec().Value.Extensions = extensions
}

// Create implements resource.Resource.
func (r *machineExtensionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineExtensionsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	id := machineExtensionsID(r.targetSuffix(plan))

	config := omni.NewExtensionsConfiguration(id)

	for _, l := range r.scopeLabels(plan) {
		config.Metadata().Labels().Set(l.F1, l.F2)
	}

	r.applyExtensions(ctx, plan, config, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.data.state.Create(ctx, config); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni extensions configuration", err)

		return
	}

	plan.ID = types.StringValue(id)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *machineExtensionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineExtensionsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	config, err := safe.ReaderGetByID[*omni.ExtensionsConfiguration](ctx, r.data.state, state.ID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni extensions configuration", err)

		return
	}

	r.configToModel(ctx, config, &state, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Only the extensions list is mutable; the scope forces
// replacement.
func (r *machineExtensionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan machineExtensionsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewExtensionsConfiguration(plan.ID.ValueString()).Metadata(),
		func(config *omni.ExtensionsConfiguration) error {
			r.applyExtensions(ctx, plan, config, &resp.Diagnostics)

			if resp.Diagnostics.HasError() {
				return errExtensionsPlanInvalid
			}

			return nil
		})
	if err != nil {
		if resp.Diagnostics.HasError() {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to update Omni extensions configuration", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *machineExtensionsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineExtensionsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	config := omni.NewExtensionsConfiguration(state.ID.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, config.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni extensions configuration", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Extensions configurations are imported by
// their ID.
func (r *machineExtensionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// configToModel populates the model from an ExtensionsConfiguration resource read from Omni.
func (r *machineExtensionsResource) configToModel(ctx context.Context, config *omni.ExtensionsConfiguration, model *machineExtensionsResourceModel, diags *diag.Diagnostics) {
	model.ID = types.StringValue(config.Metadata().ID())

	if cluster, ok := config.Metadata().Labels().Get(omni.LabelCluster); ok {
		model.Cluster = types.StringValue(cluster)
	}

	// Rebuild the selector from the narrowing labels. It stays null unless one of them is present, so
	// a plain cluster-scoped configuration keeps an unset selector and does not produce spurious diffs.
	var selector machineExtensionsSelectorModel

	machineSet, hasMachineSet := config.Metadata().Labels().Get(omni.LabelMachineSet)
	clusterMachine, hasClusterMachine := config.Metadata().Labels().Get(omni.LabelClusterMachine)

	if hasMachineSet {
		selector.MachineSet = types.StringValue(machineSet)
	}

	if hasClusterMachine {
		selector.ClusterMachine = types.StringValue(clusterMachine)
	}

	if hasMachineSet || hasClusterMachine {
		model.Selector = &selector
	}

	// GetExtensions returns a nil slice for a configuration with no extensions, which SetValueFrom
	// would turn into a null set. The attribute is required and an empty set is a valid value (it
	// clears all extensions), so normalize nil to an empty set to keep an empty configuration stable.
	extensions := config.TypedSpec().Value.GetExtensions()
	if extensions == nil {
		extensions = []string{}
	}

	set, d := types.SetValueFrom(ctx, types.StringType, extensions)
	diags.Append(d...)

	model.Extensions = set
}
