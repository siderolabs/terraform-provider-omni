// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/gen/pair"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// defaultConfigPatchWeight is the default weight applied to user config patches.
const defaultConfigPatchWeight = 400

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                     = (*configPatchResource)(nil)
	_ resource.ResourceWithConfigure        = (*configPatchResource)(nil)
	_ resource.ResourceWithImportState      = (*configPatchResource)(nil)
	_ resource.ResourceWithConfigValidators = (*configPatchResource)(nil)
)

// configPatchResourceModel maps the omni_config_patch resource schema.
type configPatchResourceModel struct {
	Selector *configPatchSelectorModel `tfsdk:"selector"`
	ID       types.String              `tfsdk:"id"`
	Name     types.String              `tfsdk:"name"`
	Data     types.String              `tfsdk:"data"`
	Cluster  types.String              `tfsdk:"cluster"`
	Weight   types.Int64               `tfsdk:"weight"`
}

// configPatchSelectorModel maps the nested `selector` block: it narrows a `cluster`-scoped patch to
// a single machine set or cluster machine, or targets a single bare machine. At most one field may
// be set. `machine_set` and `cluster_machine` require `cluster`; `machine` must not be combined with
// `cluster`.
type configPatchSelectorModel struct {
	MachineSet     types.String `tfsdk:"machine_set"`
	ClusterMachine types.String `tfsdk:"cluster_machine"`
	Machine        types.String `tfsdk:"machine"`
}

// configPatchResource implements the omni_config_patch resource.
type configPatchResource struct {
	data *providerData
}

// NewConfigPatchResource returns a new omni_config_patch resource.
func NewConfigPatchResource() resource.Resource {
	return &configPatchResource{}
}

// Metadata implements resource.Resource.
func (r *configPatchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config_patch"
}

// Schema implements resource.Resource.
func (r *configPatchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Omni config patch: a Talos machine configuration patch applied to a cluster, machine set, " +
			"cluster machine or a single machine. Set `cluster` to target a whole cluster and optionally narrow it with a " +
			"`selector` block (a machine set or cluster machine of that cluster), or set `selector.machine` to target a " +
			"single machine that is not part of a cluster.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The generated config patch ID, composed as `<zero-padded weight>-<target>-<name>`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The patch name, used together with the weight to form the resource ID. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"weight": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(defaultConfigPatchWeight),
				Description: "The patch weight. Lower-weight patches are applied first. Defaults to 400. " +
					"Changing the weight changes the resource ID and replaces the patch.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"data": schema.StringAttribute{
				Required:    true,
				Description: "The Talos machine configuration patch, as YAML. Omni-controlled fields are rejected.",
			},
			"cluster": schema.StringAttribute{
				Optional:    true,
				Description: "Apply the patch to all machines of this cluster. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"selector": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Narrows the target within a `cluster`, or targets a single bare machine. At most one of its " +
					"fields may be set. `machine_set` and `cluster_machine` require `cluster`; `machine` must not be combined " +
					"with `cluster`. Immutable.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"machine_set": schema.StringAttribute{
						Optional:    true,
						Description: "Apply the patch to all machines of this machine set of the cluster.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"cluster_machine": schema.StringAttribute{
						Optional:    true,
						Description: "Apply the patch to this single cluster machine (by machine UUID).",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"machine": schema.StringAttribute{
						Optional:    true,
						Description: "Apply the patch to this single machine (by machine UUID) that is not part of a cluster.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},
		},
	}
}

// ConfigValidators implements resource.ResourceWithConfigValidators.
func (r *configPatchResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		// A patch has exactly one root scope: either a `cluster` (optionally narrowed by the
		// selector) or a single bare `machine`. This also rejects an empty config and rejects
		// `machine_set`/`cluster_machine` without a `cluster` (neither root would then be set).
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("cluster"),
			path.MatchRoot("selector").AtName("machine"),
		),
		// Within the selector, at most one narrowing/target field may be set.
		resourcevalidator.Conflicting(
			path.MatchRoot("selector").AtName("machine_set"),
			path.MatchRoot("selector").AtName("cluster_machine"),
			path.MatchRoot("selector").AtName("machine"),
		),
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *configPatchResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// configPatchID composes the resource ID from the weight and name.
func configPatchID(weight int64, cluster, name string) string {
	return fmt.Sprintf("%03d-%s-%s", weight, cluster, name)
}

// parseConfigPatchID splits a `<weight>-<name>` ID back into its weight and name. When the ID does
// not carry a numeric prefix, the weight defaults to 0 and the whole ID is treated as the name.
func parseConfigPatchID(id string) (int64, string) {
	prefix, name, found := strings.Cut(id, "-")
	if !found {
		return 0, id
	}

	weight, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, id
	}

	return weight, name
}

// scopeLabels returns the scope labels derived from the model. A cluster-scoped patch carries the
// cluster label, optionally narrowed by a machine set or cluster machine label; a bare machine patch
// carries only the machine label.
func (r *configPatchResource) scopeLabels(plan configPatchResourceModel) []pair.Pair[string, string] {
	var labels []pair.Pair[string, string]

	if v := plan.Cluster.ValueString(); v != "" {
		labels = append(labels, pair.MakePair(omni.LabelCluster, v))
	}

	if plan.Selector != nil {
		if v := plan.Selector.MachineSet.ValueString(); v != "" {
			labels = append(labels, pair.MakePair(omni.LabelMachineSet, v))
		}

		if v := plan.Selector.ClusterMachine.ValueString(); v != "" {
			labels = append(labels, pair.MakePair(omni.LabelClusterMachine, v))
		}

		if v := plan.Selector.Machine.ValueString(); v != "" {
			labels = append(labels, pair.MakePair(omni.LabelMachine, v))
		}
	}

	return labels
}

// targetSuffix returns the narrowest scope identifier configured on the model, used to compose a
// stable and collision-free resource ID.
func (r *configPatchResource) targetSuffix(plan configPatchResourceModel) string {
	if plan.Selector != nil {
		if v := plan.Selector.Machine.ValueString(); v != "" {
			return v
		}

		if v := plan.Selector.ClusterMachine.ValueString(); v != "" {
			return v
		}

		if v := plan.Selector.MachineSet.ValueString(); v != "" {
			return v
		}
	}

	return plan.Cluster.ValueString()
}

// Create implements resource.Resource.
func (r *configPatchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configPatchResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	id := configPatchID(plan.Weight.ValueInt64(), r.targetSuffix(plan), plan.Name.ValueString())

	patch := omni.NewConfigPatch(id, r.scopeLabels(plan)...)
	patch.Metadata().Annotations().Set(omni.ConfigPatchName, plan.Name.ValueString())

	if err := r.applyData(plan, patch); err != nil {
		errToDiag(&resp.Diagnostics, "Invalid Omni config patch", err)

		return
	}

	if err := r.data.state.Create(ctx, patch); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni config patch", err)

		return
	}

	plan.ID = types.StringValue(id)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// applyData validates and stores the patch data.
func (r *configPatchResource) applyData(plan configPatchResourceModel, patch *omni.ConfigPatch) error {
	data := []byte(plan.Data.ValueString())

	if err := omni.ValidateConfigPatch(data); err != nil {
		return err
	}

	return patch.TypedSpec().Value.SetUncompressedData(data)
}

// Read implements resource.Resource.
func (r *configPatchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configPatchResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	patch, err := safe.ReaderGetByID[*omni.ConfigPatch](ctx, r.data.state, state.ID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni config patch", err)

		return
	}

	buffer, err := patch.TypedSpec().Value.GetUncompressedData()
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read Omni config patch data", err)

		return
	}

	data := string(buffer.Data())
	buffer.Free()

	state.Data = types.StringValue(data)

	// Recover name and weight from the ID (`<weight>-<name>`) and the name annotation, so the
	// resource can be imported by ID alone.
	weight, name := parseConfigPatchID(patch.Metadata().ID())
	if annotation, ok := patch.Metadata().Annotations().Get(omni.ConfigPatchName); ok {
		name = annotation
	}

	state.ID = types.StringValue(patch.Metadata().ID())
	state.Name = types.StringValue(name)
	state.Weight = types.Int64Value(weight)

	if cluster, ok := patch.Metadata().Labels().Get(omni.LabelCluster); ok {
		state.Cluster = types.StringValue(cluster)
	}

	// Rebuild the selector from the narrowing labels. It stays null unless one of them is present,
	// so a plain cluster-scoped patch keeps an unset selector and does not produce spurious diffs.
	var selector configPatchSelectorModel

	machineSet, hasMachineSet := patch.Metadata().Labels().Get(omni.LabelMachineSet)
	clusterMachine, hasClusterMachine := patch.Metadata().Labels().Get(omni.LabelClusterMachine)
	machine, hasMachine := patch.Metadata().Labels().Get(omni.LabelMachine)

	if hasMachineSet {
		selector.MachineSet = types.StringValue(machineSet)
	}

	if hasClusterMachine {
		selector.ClusterMachine = types.StringValue(clusterMachine)
	}

	if hasMachine {
		selector.Machine = types.StringValue(machine)
	}

	if hasMachineSet || hasClusterMachine || hasMachine {
		state.Selector = &selector
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Only the patch data is mutable; everything else forces
// replacement.
func (r *configPatchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configPatchResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewConfigPatch(plan.ID.ValueString()).Metadata(),
		func(patch *omni.ConfigPatch) error {
			return r.applyData(plan, patch)
		})
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni config patch", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *configPatchResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configPatchResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	patch := omni.NewConfigPatch(state.ID.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, patch.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni config patch", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Patches are imported by their ID.
func (r *configPatchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
