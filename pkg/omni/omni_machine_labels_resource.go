// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"

	cosiresource "github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/jointoken"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                   = (*machineLabelsResource)(nil)
	_ resource.ResourceWithConfigure      = (*machineLabelsResource)(nil)
	_ resource.ResourceWithImportState    = (*machineLabelsResource)(nil)
	_ resource.ResourceWithValidateConfig = (*machineLabelsResource)(nil)
)

// machineLabelsResourceModel maps the omni_machine_labels resource schema.
type machineLabelsResourceModel struct {
	MachineID types.String `tfsdk:"machine_id"`
	Labels    types.Map    `tfsdk:"labels"`
}

// machineLabelsResource implements the omni_machine_labels resource.
//
// Omni keeps all the user labels of a machine in one MachineLabels resource, and writes to it
// itself: it seeds the labels a join token carries when the machine first registers, and the tags
// of the cloud platform the machine runs on. The resource therefore manages only the keys it is
// given, and leaves every other label on the machine alone.
type machineLabelsResource struct {
	data *providerData
}

// NewMachineLabelsResource returns a new omni_machine_labels resource.
func NewMachineLabelsResource() resource.Resource {
	return &machineLabelsResource{}
}

// Metadata implements resource.Resource.
func (r *machineLabelsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_labels"
}

// Schema implements resource.Resource.
func (r *machineLabelsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages user labels on a single machine, which machine classes and label selectors match machines by.\n\n" +
			"The resource manages only the labels it is given. A machine can carry others, which it keeps: the labels " +
			"of the join token it registered with, the tags of the cloud platform it runs on, and labels set in the UI " +
			"or by another `omni_machine_labels` resource. Setting a key that is already on the machine takes it over, " +
			"and destroying the resource removes only its own keys.",
		Attributes: map[string]schema.Attribute{
			"machine_id": schema.StringAttribute{
				Required:    true,
				Description: "The machine UUID the labels apply to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"labels": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "The labels to set on the machine. Keys must not use the `omni.sidero.dev/` prefix, which is " +
					"reserved for the labels Omni sets itself.",
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
				},
			},
		},
	}
}

// ValidateConfig implements resource.ResourceWithValidateConfig. Checking the labels here turns
// what Omni would refuse into an error on the attribute at plan time.
func (r *machineLabelsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config machineLabelsResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() || config.Labels.IsNull() || config.Labels.IsUnknown() {
		return
	}

	// Values not known yet are left to apply time; the keys of a map are always known.
	labels := map[string]string{}

	for key, value := range config.Labels.Elements() {
		if str, ok := value.(types.String); ok && !str.IsUnknown() && !str.IsNull() {
			labels[key] = str.ValueString()
		} else {
			labels[key] = ""
		}
	}

	if err := jointoken.ValidateLabels(labels); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("labels"), "Invalid machine labels", err.Error())
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineLabelsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *machineLabelsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineLabelsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	desired := labelsFromModel(ctx, plan.Labels, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.modify(ctx, plan.MachineID.ValueString(), nil, desired); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to set Omni machine labels", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource. It reports only the managed keys that are still on the machine,
// with their current values, so that a label changed or removed outside Terraform shows as drift.
func (r *machineLabelsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineLabelsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	machineLabels, err := safe.ReaderGetByID[*omni.MachineLabels](ctx, r.data.state, state.MachineID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni machine labels", err)

		return
	}

	current := machineLabels.Metadata().Labels().Raw()

	var managed map[string]string

	// An import starts with no labels in state, and adopts every label on the machine that the
	// configuration could hold.
	if state.Labels.IsNull() || state.Labels.IsUnknown() {
		managed = adoptableLabels(current)
	} else {
		managed = managedLabels(current, labelsFromModel(ctx, state.Labels, &resp.Diagnostics))
	}

	if resp.Diagnostics.HasError() {
		return
	}

	if len(managed) == 0 {
		// An import has nothing to adopt: say so, rather than let Terraform report the machine's
		// labels as missing.
		if state.Labels.IsNull() || state.Labels.IsUnknown() {
			resp.Diagnostics.AddError("No machine labels to import",
				fmt.Sprintf("Machine %q has no labels that omni_machine_labels can manage: it has none, or only "+
					"labels reserved by Omni or not valid in the configuration.", state.MachineID.ValueString()))

			return
		}

		resp.State.RemoveResource(ctx)

		return
	}

	labels, diags := types.MapValueFrom(ctx, types.StringType, managed)
	resp.Diagnostics.Append(diags...)

	state.MachineID = types.StringValue(machineLabels.Metadata().ID())
	state.Labels = labels

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. The machine forces replacement; the labels are updated in
// place, removing the managed keys no longer configured.
func (r *machineLabelsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state machineLabelsResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	desired := labelsFromModel(ctx, plan.Labels, &resp.Diagnostics)
	previous := labelsFromModel(ctx, state.Labels, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.modify(ctx, plan.MachineID.ValueString(), previous, desired); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni machine labels", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource. It removes the managed keys, and the whole MachineLabels
// resource once no label is left on it, as the UI does when the last label is removed.
func (r *machineLabelsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineLabelsResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	previous := labelsFromModel(ctx, state.Labels, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	id := state.MachineID.ValueString()

	var empty bool

	// Removing the last label tears the resource down in the same versioned write, so that a label
	// another writer adds meanwhile either lands before it, and keeps the resource, or fails against
	// the tearing down resource, rather than being destroyed along with it.
	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewMachineLabels(id).Metadata(), func(machineLabels *omni.MachineLabels) error {
		updateManagedLabels(machineLabels.Metadata().Labels(), previous, nil)

		empty = machineLabels.Metadata().Labels().Empty()
		if empty {
			machineLabels.Metadata().SetPhase(cosiresource.PhaseTearingDown)
		}

		return nil
	})
	switch {
	case cosistate.IsNotFoundError(err): // Omni removes the labels together with the machine.
		return
	case cosistate.IsPhaseConflictError(err):
		// Already being torn down: by an earlier attempt of this Delete, which failed before the
		// destroy, or by Omni removing the machine. Either way it is destroyed with whatever labels
		// it holds, which no writer can change any more, and the managed labels go with it.
	case err != nil:
		errToDiag(&resp.Diagnostics, "Failed to remove Omni machine labels", err)

		return
	case !empty:
		return
	}

	// Tearing down an already tearing down resource is not an error, and the call waits for any
	// finalizer on it to be released before destroying it.
	if err = r.data.state.TeardownAndDestroy(ctx, omni.NewMachineLabels(id).Metadata()); err != nil && !cosistate.IsNotFoundError(err) {
		errToDiag(&resp.Diagnostics, "Failed to remove Omni machine labels", err)
	}
}

// ImportState implements resource.ResourceWithImportState. The labels are imported by the machine
// UUID, and the import adopts every label on the machine except those the configuration could not
// hold, such as keys under Omni's reserved prefix, which are left unmanaged.
func (r *machineLabelsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("machine_id"), req, resp)
}

// modify writes the desired labels onto the machine's MachineLabels, creating it when the machine
// has none yet, and removes the previously managed keys that are no longer desired.
func (r *machineLabelsResource) modify(ctx context.Context, id string, previous, desired map[string]string) error {
	return safe.StateModify(ctx, r.data.state, omni.NewMachineLabels(id), func(machineLabels *omni.MachineLabels) error {
		updateManagedLabels(machineLabels.Metadata().Labels(), previous, desired)

		return nil
	})
}

// updateManagedLabels removes the keys of previous missing from desired, and sets desired. Labels
// in neither are left alone.
func updateManagedLabels(labels *cosiresource.Labels, previous, desired map[string]string) {
	for key := range previous {
		if _, ok := desired[key]; !ok {
			labels.Delete(key)
		}
	}

	for key, value := range desired {
		labels.Set(key, value)
	}
}

// managedLabels returns the managed keys still present in current, with their current values.
func managedLabels(current, managed map[string]string) map[string]string {
	result := make(map[string]string, len(managed))

	for key := range managed {
		if value, ok := current[key]; ok {
			result[key] = value
		}
	}

	return result
}

// adoptableLabels returns the labels of current that are valid in the configuration.
func adoptableLabels(current map[string]string) map[string]string {
	result := make(map[string]string, len(current))

	for key, value := range current {
		if jointoken.ValidateLabels(map[string]string{key: value}) == nil {
			result[key] = value
		}
	}

	return result
}

// labelsFromModel reads a labels map attribute.
func labelsFromModel(ctx context.Context, value types.Map, diags *diag.Diagnostics) map[string]string {
	labels := map[string]string{}

	if value.IsNull() || value.IsUnknown() {
		return labels
	}

	diags.Append(value.ElementsAs(ctx, &labels, false)...)

	return labels
}
