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
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

const (
	machineSetRoleControlPlane = "controlplane"
	machineSetRoleWorker       = "workers"
)

// machineSetAllocationNames are the allocation types accepted by the `allocation_type` attribute,
// and machineSetAllocationStatic is the one Omni falls back to.
//
// Like machineSetStrategyNames, they come from the generated enum rather than a hand-written list:
// machineSetSpecFromPlan looks each one back up in MachineSetSpec_MachineAllocation_Type_value,
// where a missing key yields 0, so a list that drifted from the enum would not fail, it would
// silently mean Static.
var (
	machineSetAllocationNames  = enumNames(specs.MachineSetSpec_MachineAllocation_Type_name)
	machineSetAllocationStatic = enumName(specs.MachineSetSpec_MachineAllocation_Type_name, int32(specs.MachineSetSpec_MachineAllocation_Static))
)

// machineSetStrategyNames are the strategy types accepted by the `type` attribute.
//
// They come from the generated enum rather than a hand-written list because strategyToSpec looks
// each one back up in MachineSetSpec_UpdateStrategy_value, where a missing key yields 0: a list that
// drifted from the enum would not fail, it would silently mean Unset.
var machineSetStrategyNames = enumNames(specs.MachineSetSpec_UpdateStrategy_name)

// Ensure the resource satisfies the framework interfaces.
var (
	_ frameworkresource.Resource                = (*machineSetResource)(nil)
	_ frameworkresource.ResourceWithConfigure   = (*machineSetResource)(nil)
	_ frameworkresource.ResourceWithImportState = (*machineSetResource)(nil)
)

// machineSetStrategyModel maps an update/delete/upgrade strategy block.
type machineSetStrategyModel struct {
	Type           types.String `tfsdk:"type"`
	MaxParallelism types.Int64  `tfsdk:"max_parallelism"`
}

// machineSetMachineClassModel maps the machine_class block.
type machineSetMachineClassModel struct {
	Name           types.String `tfsdk:"name"`
	AllocationType types.String `tfsdk:"allocation_type"`
	Size           types.Int64  `tfsdk:"size"`
}

// machineSetBootstrapSpecModel maps the bootstrap_spec block.
type machineSetBootstrapSpecModel struct {
	ClusterUUID types.String `tfsdk:"cluster_uuid"`
	Snapshot    types.String `tfsdk:"snapshot"`
}

// machineSetResourceModel maps the omni_machine_set resource schema.
type machineSetResourceModel struct {
	UpdateStrategy  *machineSetStrategyModel      `tfsdk:"update_strategy"`
	DeleteStrategy  *machineSetStrategyModel      `tfsdk:"delete_strategy"`
	UpgradeStrategy *machineSetStrategyModel      `tfsdk:"upgrade_strategy"`
	MachineClass    *machineSetMachineClassModel  `tfsdk:"machine_class"`
	BootstrapSpec   *machineSetBootstrapSpecModel `tfsdk:"bootstrap_spec"`
	ID              types.String                  `tfsdk:"id"`
	Name            types.String                  `tfsdk:"name"`
	Cluster         types.String                  `tfsdk:"cluster"`
	Role            types.String                  `tfsdk:"role"`
}

// machineSetResource implements the omni_machine_set resource.
type machineSetResource struct {
	data *providerData
}

// NewMachineSetResource returns a new omni_machine_set resource.
func NewMachineSetResource() frameworkresource.Resource {
	return &machineSetResource{}
}

// Metadata implements resource.Resource.
func (r *machineSetResource) Metadata(_ context.Context, req frameworkresource.MetadataRequest, resp *frameworkresource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_set"
}

// Schema implements resource.Resource.
func (r *machineSetResource) Schema(_ context.Context, _ frameworkresource.SchemaRequest, resp *frameworkresource.SchemaResponse) {
	strategyAttribute := func(description string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			Optional:    true,
			Description: description,
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					Required:    true,
					Description: "Strategy type. " + describeOneOf(machineSetStrategyNames),
					Validators: []validator.String{
						stringvalidator.OneOf(machineSetStrategyNames...),
					},
				},
				"max_parallelism": schema.Int64Attribute{
					Optional:    true,
					Description: "Maximum number of machines updated in parallel (Rolling strategy only).",
				},
			},
		}
	}

	resp.Schema = schema.Schema{
		Description: "Manages an Omni machine set: a group of machines within a cluster that share a role " +
			"(control plane or workers) and update behavior.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "The generated machine set ID, composed as `<cluster>-<name>`. Use this to reference the " +
					"machine set from other resources, such as `omni_machine_set_node`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The machine set name, appended to the cluster name to form the resource ID. Must not be set " +
					"for control planes, and defaults to `workers` for the default worker machine set. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster": schema.StringAttribute{
				Required:    true,
				Description: "The cluster this machine set belongs to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				Required:    true,
				Description: "The role of the machines in this set. One of: controlplane, workers. A cluster may have only one control plane machine set. Immutable.",
				Validators: []validator.String{
					stringvalidator.OneOf(machineSetRoleControlPlane, machineSetRoleWorker),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"update_strategy":  strategyAttribute("Strategy used when updating machine configuration. Defaults to Rolling."),
			"delete_strategy":  strategyAttribute("Strategy used when removing machines from the set."),
			"upgrade_strategy": strategyAttribute("Strategy used when upgrading Talos on the machines."),
			"machine_class": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Automatically allocate machines from a machine class instead of assigning them explicitly " +
					"with omni_machine_set_node resources. Mutually exclusive with omni_machine_set_node.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Required:    true,
						Description: "The machine class name to allocate machines from.",
					},
					"size": schema.Int64Attribute{
						Optional:    true,
						Description: "Number of machines to allocate. Required for Static allocation, ignored for Unlimited.",
					},
					"allocation_type": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Description: "Allocation type. " + describeOneOf(machineSetAllocationNames) +
							" Defaults to `" + machineSetAllocationStatic + "`.",
						Validators: []validator.String{
							stringvalidator.OneOf(machineSetAllocationNames...),
						},
					},
				},
			},
			"bootstrap_spec": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Restore the cluster etcd from a backup on bootstrap. Only valid for the control plane machine set. " +
					"Immutable.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"cluster_uuid": schema.StringAttribute{
						Required:    true,
						Description: "UUID of the cluster to restore the etcd backup from.",
					},
					"snapshot": schema.StringAttribute{
						Required:    true,
						Description: "Name of the etcd backup snapshot to restore from.",
					},
				},
			},
		},
	}
}

// ValidateConfig implements resource.ResourceWithValidateConfig.
func (r *machineSetResource) ValidateConfig(ctx context.Context, req frameworkresource.ValidateConfigRequest, resp *frameworkresource.ValidateConfigResponse) {
	var config machineSetResourceModel

	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	// machine_class.size is required for Static allocation (the default). Without this guard a null
	// size is silently sent to Omni as 0, allocating zero machines.
	if mc := config.MachineClass; mc != nil {
		allocationType := mc.AllocationType.ValueString()
		if (allocationType == "" || allocationType == machineSetAllocationStatic) && mc.Size.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("machine_class").AtName("size"),
				"Invalid Attribute Configuration",
				fmt.Sprintf("The 'machine_class.size' field is required when 'allocation_type' is %q.", machineSetAllocationStatic),
			)
		}
	}

	if config.Role.IsUnknown() || config.Role.IsNull() {
		return
	}

	switch config.Role.ValueString() {
	case machineSetRoleControlPlane:
		if !config.Name.IsNull() && !config.Name.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("name"),
				"Invalid Attribute Configuration",
				fmt.Sprintf("The 'name' field cannot be set when 'role' is %q.", config.Role.ValueString()),
			)
		}
	case machineSetRoleWorker:
		if config.BootstrapSpec != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("bootstrap_spec"),
				"Invalid Attribute Configuration",
				fmt.Sprintf("The 'bootstrap_spec' block is only valid when 'role' is %q.", machineSetRoleControlPlane),
			)
		}
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineSetResource) Configure(_ context.Context, req frameworkresource.ConfigureRequest, resp *frameworkresource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// applyMachineSetModel populates a MachineSet resource from the model.
func (r *machineSetResource) applyMachineSetModel(plan machineSetResourceModel, machineSet *omni.MachineSet) error {
	value := machineSet.TypedSpec().Value

	// Update strategy defaults to Rolling when not specified, matching Omni's behavior.
	value.UpdateStrategy = specs.MachineSetSpec_Rolling
	value.UpdateStrategyConfig = nil

	value.DeleteStrategy = specs.MachineSetSpec_Unset
	value.DeleteStrategyConfig = nil

	value.UpgradeStrategy = specs.MachineSetSpec_Unset
	value.UpgradeStrategyConfig = nil

	if plan.UpdateStrategy != nil {
		value.UpdateStrategy, value.UpdateStrategyConfig = strategyToSpec(plan.UpdateStrategy)
	}

	if plan.DeleteStrategy != nil {
		value.DeleteStrategy, value.DeleteStrategyConfig = strategyToSpec(plan.DeleteStrategy)
	}

	if plan.UpgradeStrategy != nil {
		value.UpgradeStrategy, value.UpgradeStrategyConfig = strategyToSpec(plan.UpgradeStrategy)
	}

	value.MachineAllocation = nil

	if plan.MachineClass != nil {
		// An unset attribute is not in the value map, which yields 0: Static, the documented default.
		allocationType := specs.MachineSetSpec_MachineAllocation_Type(
			specs.MachineSetSpec_MachineAllocation_Type_value[plan.MachineClass.AllocationType.ValueString()],
		)

		value.MachineAllocation = &specs.MachineSetSpec_MachineAllocation{
			Name:           plan.MachineClass.Name.ValueString(),
			MachineCount:   uint32(plan.MachineClass.Size.ValueInt64()),
			AllocationType: allocationType,
		}
	}

	value.BootstrapSpec = nil
	if plan.BootstrapSpec != nil {
		if plan.Role.ValueString() != machineSetRoleControlPlane {
			return fmt.Errorf("bootstrap_spec is only valid for a control plane machine set")
		}

		value.BootstrapSpec = &specs.MachineSetSpec_BootstrapSpec{
			ClusterUuid: plan.BootstrapSpec.ClusterUUID.ValueString(),
			Snapshot:    plan.BootstrapSpec.Snapshot.ValueString(),
		}
	}

	return nil
}

// setMachineSetLabels sets the cluster and role labels on the machine set.
func setMachineSetLabels(machineSet *omni.MachineSet, cluster, role string) {
	machineSet.Metadata().Labels().Set(omni.LabelCluster, cluster)

	switch role {
	case machineSetRoleControlPlane:
		machineSet.Metadata().Labels().Set(omni.LabelControlPlaneRole, "")
	case machineSetRoleWorker:
		machineSet.Metadata().Labels().Set(omni.LabelWorkerRole, "")
	}
}

// Create implements resource.Resource.
func (r *machineSetResource) Create(ctx context.Context, req frameworkresource.CreateRequest, resp *frameworkresource.CreateResponse) {
	var plan machineSetResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	machineSet := omni.NewMachineSet(machineSetID(plan))
	setMachineSetLabels(machineSet, plan.Cluster.ValueString(), plan.Role.ValueString())

	if err := r.applyMachineSetModel(plan, machineSet); err != nil {
		errToDiag(&resp.Diagnostics, "Invalid Omni machine set", err)

		return
	}

	if err := r.data.state.Create(ctx, machineSet); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni machine set", err)

		return
	}

	r.machineSetToModel(machineSet, &plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// machineSetID returns the Omni resource ID of the planned machine set. Omni prefixes it with the
// cluster name, so it is kept out of the `name` attribute, which holds the configured name only.
func machineSetID(plan machineSetResourceModel) string {
	if plan.Role.ValueString() == machineSetRoleControlPlane {
		return omni.ControlPlanesResourceID(plan.Cluster.ValueString())
	}

	if plan.Name.IsUnknown() || plan.Name.IsNull() {
		return omni.WorkersResourceID(plan.Cluster.ValueString())
	}

	return omni.AdditionalWorkersResourceID(plan.Cluster.ValueString(), plan.Name.ValueString())
}

// machineSetNameFromID strips the cluster prefix Omni adds to machine set IDs, recovering the name
// so that refreshed and imported state matches the configuration. Control planes and default
// workers come back as their well-known suffix.
func machineSetNameFromID(id, cluster string) string {
	if cluster == "" {
		return id
	}

	return strings.TrimPrefix(id, cluster+"-")
}

// Read implements resource.Resource.
func (r *machineSetResource) Read(ctx context.Context, req frameworkresource.ReadRequest, resp *frameworkresource.ReadResponse) {
	var state machineSetResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// State written by provider versions that stored the Omni resource ID in `name` has no `id` yet;
	// fall back to it so such state is refreshed instead of being dropped and recreated.
	id := state.ID.ValueString()
	if id == "" {
		id = state.Name.ValueString()
	}

	machineSet, err := safe.ReaderGetByID[*omni.MachineSet](ctx, r.data.state, id)
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni machine set", err)

		return
	}

	r.machineSetToModel(machineSet, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
func (r *machineSetResource) Update(ctx context.Context, req frameworkresource.UpdateRequest, resp *frameworkresource.UpdateResponse) {
	var plan machineSetResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	machineSet, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewMachineSet(plan.ID.ValueString()).Metadata(),
		func(machineSet *omni.MachineSet) error {
			setMachineSetLabels(machineSet, plan.Cluster.ValueString(), plan.Role.ValueString())

			return r.applyMachineSetModel(plan, machineSet)
		})
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni machine set", err)

		return
	}

	r.machineSetToModel(machineSet, &plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *machineSetResource) Delete(ctx context.Context, req frameworkresource.DeleteRequest, resp *frameworkresource.DeleteResponse) {
	var state machineSetResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	machineSet := omni.NewMachineSet(state.ID.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, machineSet.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni machine set", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Machine sets are imported by their full
// Omni resource ID, e.g. `my-cluster-extra-workers`.
func (r *machineSetResource) ImportState(ctx context.Context, req frameworkresource.ImportStateRequest, resp *frameworkresource.ImportStateResponse) {
	frameworkresource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// machineSetToModel populates the model from a MachineSet resource read from Omni.
func (r *machineSetResource) machineSetToModel(machineSet *omni.MachineSet, model *machineSetResourceModel) {
	value := machineSet.TypedSpec().Value

	model.ID = types.StringValue(machineSet.Metadata().ID())

	if cluster, ok := machineSet.Metadata().Labels().Get(omni.LabelCluster); ok {
		model.Cluster = types.StringValue(cluster)
	}

	model.Name = types.StringValue(machineSetNameFromID(machineSet.Metadata().ID(), model.Cluster.ValueString()))

	if _, ok := machineSet.Metadata().Labels().Get(omni.LabelControlPlaneRole); ok {
		model.Role = types.StringValue(machineSetRoleControlPlane)
	} else if _, ok := machineSet.Metadata().Labels().Get(omni.LabelWorkerRole); ok {
		model.Role = types.StringValue(machineSetRoleWorker)
	}

	model.UpdateStrategy = strategyFromSpec(value.GetUpdateStrategy(), value.GetUpdateStrategyConfig(), model.UpdateStrategy)
	model.DeleteStrategy = strategyFromSpec(value.GetDeleteStrategy(), value.GetDeleteStrategyConfig(), model.DeleteStrategy)
	model.UpgradeStrategy = strategyFromSpec(value.GetUpgradeStrategy(), value.GetUpgradeStrategyConfig(), model.UpgradeStrategy)

	if allocation := value.GetMachineAllocation(); allocation != nil {
		allocationType := enumName(specs.MachineSetSpec_MachineAllocation_Type_name, int32(allocation.GetAllocationType()))

		size := types.Int64Null()
		if model.MachineClass != nil && !model.MachineClass.Size.IsNull() {
			size = types.Int64Value(int64(allocation.GetMachineCount()))
		} else if allocation.GetMachineCount() > 0 {
			size = types.Int64Value(int64(allocation.GetMachineCount()))
		}

		model.MachineClass = &machineSetMachineClassModel{
			Name:           types.StringValue(allocation.GetName()),
			Size:           size,
			AllocationType: types.StringValue(allocationType),
		}
	} else {
		model.MachineClass = nil
	}

	if bootstrap := value.GetBootstrapSpec(); bootstrap != nil {
		model.BootstrapSpec = &machineSetBootstrapSpecModel{
			ClusterUUID: types.StringValue(bootstrap.GetClusterUuid()),
			Snapshot:    types.StringValue(bootstrap.GetSnapshot()),
		}
	} else {
		model.BootstrapSpec = nil
	}
}

// strategyToSpec converts a strategy model into its enum + config representation.
func strategyToSpec(model *machineSetStrategyModel) (specs.MachineSetSpec_UpdateStrategy, *specs.MachineSetSpec_UpdateStrategyConfig) {
	strategy := specs.MachineSetSpec_UpdateStrategy(specs.MachineSetSpec_UpdateStrategy_value[model.Type.ValueString()])

	var config *specs.MachineSetSpec_UpdateStrategyConfig
	if !model.MaxParallelism.IsNull() && !model.MaxParallelism.IsUnknown() {
		config = &specs.MachineSetSpec_UpdateStrategyConfig{
			Rolling: &specs.MachineSetSpec_RollingUpdateStrategyConfig{
				MaxParallelism: uint32(model.MaxParallelism.ValueInt64()),
			},
		}
	}

	return strategy, config
}

// strategyFromSpec converts an enum + config back into a strategy model, preserving the presence of
// an existing block to avoid spurious diffs.
func strategyFromSpec(
	strategy specs.MachineSetSpec_UpdateStrategy,
	config *specs.MachineSetSpec_UpdateStrategyConfig,
	existing *machineSetStrategyModel,
) *machineSetStrategyModel {
	// A strategy block that the user never configured is left absent: the server always reports a
	// concrete strategy (update defaults to Rolling), and materializing it would diverge from the
	// null value in the configuration. Drift is still surfaced for blocks the user did configure.
	if existing == nil {
		return nil
	}

	maxParallelism := types.Int64Null()
	if config != nil && config.GetRolling() != nil {
		maxParallelism = types.Int64Value(int64(config.GetRolling().GetMaxParallelism()))
	}

	return &machineSetStrategyModel{
		Type:           types.StringValue(enumName(specs.MachineSetSpec_UpdateStrategy_name, int32(strategy))),
		MaxParallelism: maxParallelism,
	}
}
