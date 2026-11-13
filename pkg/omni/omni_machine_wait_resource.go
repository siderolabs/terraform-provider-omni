// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	cosiresource "github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                     = (*machineWaitResource)(nil)
	_ resource.ResourceWithConfigure        = (*machineWaitResource)(nil)
	_ resource.ResourceWithConfigValidators = (*machineWaitResource)(nil)
)

// defaultMachineWaitTimeout covers booting a VM from an ISO and registering, with room to spare.
const defaultMachineWaitTimeout = 15 * time.Minute

// defaultMachineLeaveTimeout covers Omni resetting a machine to take it out of its cluster.
const defaultMachineLeaveTimeout = 10 * time.Minute

// errMachineAmbiguous reports labels that select more than one machine.
var errMachineAmbiguous = errors.New("the labels match more than one machine")

// machineWaitResourceModel maps the omni_machine_wait resource schema.
type machineWaitResourceModel struct {
	Labels          types.Map      `tfsdk:"labels"`
	Triggers        types.Map      `tfsdk:"triggers"`
	MachineID       types.String   `tfsdk:"machine_id"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
	Connected       types.Bool     `tfsdk:"connected"`
	RemoveOnDestroy types.Bool     `tfsdk:"remove_on_destroy"`
}

// machineWaitResource implements the omni_machine_wait resource.
type machineWaitResource struct {
	data *providerData
}

// NewMachineWaitResource returns a new omni_machine_wait resource.
func NewMachineWaitResource() resource.Resource {
	return &machineWaitResource{}
}

// Metadata implements resource.Resource.
func (r *machineWaitResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_wait"
}

// Schema implements resource.Resource.
func (r *machineWaitResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Waits for a machine to register with Omni and connect, and records its UUID.\n\n" +
			"It is the bridge between whatever creates a machine, a VM on a hypervisor or a cloud instance, and the " +
			"Omni resources that need the machine's UUID, such as `omni_machine_set_node`. Select the machine by " +
			"`labels` when its UUID is not known in advance: give each machine a unique label through the " +
			"`machine_labels` of `omni_machine_join_config`, and wait on that label here. Select it by `machine_id` " +
			"when the UUID is known, for instance because it was set as the VM's SMBIOS UUID.\n\n" +
			"Only creating this resource waits. Refreshing it looks the machine up once and never blocks a plan: " +
			"if the machine has gone from Omni the resource is planned for creation again, which waits for it anew. " +
			"Destroying it leaves the machine registered unless `remove_on_destroy` is set.",
		Attributes: map[string]schema.Attribute{
			"labels": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Wait for the one connected machine carrying all of these labels. Disconnected machines " +
					"are not considered, so a machine left registered by a VM that has since been replaced does not get " +
					"in the way of its replacement, which joins with the same labels. More than one connected match is " +
					"an error rather than something to wait out, since it means the labels do not identify a machine. " +
					"Mutually exclusive with `machine_id`.",
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
					mapvalidator.KeysAre(stringvalidator.LengthAtLeast(1)),
				},
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"machine_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The UUID of the machine. Set it to wait for a machine whose UUID is known; otherwise it " +
					"is filled in with the UUID of the machine `labels` selected.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"triggers": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Arbitrary values that, when changed, make Terraform wait for the machine again. Pass the " +
					"ID of the VM or instance backing the machine, so that replacing it waits for its replacement " +
					"rather than reusing the UUID of the old one.",
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"remove_on_destroy": schema.BoolAttribute{
				Optional: true,
				Description: "Remove the machine from Omni when this resource is destroyed, as `omnictl cluster machine " +
					"delete` or removing it in the UI does. Set it when the machine is torn down together with this " +
					"resource, a VM destroyed in the same apply, so that Omni does not keep a disconnected machine " +
					"around. A machine that boots again afterwards simply registers anew.\n\n" +
					"If the machine is still in a cluster, destroying first waits for Omni to take it out, which " +
					"resets it, so it has to be running for that. Whatever runs the machine depends on this resource " +
					"through `triggers` or `labels` and is destroyed after it, so the machine is only turned off once it " +
					"has left the cluster. Turned off before, it would never be reset, and Omni would never finish " +
					"removing it from the cluster.",
			},
			"connected": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the machine is connected to Omni. Always true right after the wait; a later " +
					"refresh reports what Omni says at that time.",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create:            true,
				CreateDescription: fmt.Sprintf("How long to wait for the machine, as a Go duration. Defaults to %s.", defaultMachineWaitTimeout),
				Delete:            true,
				DeleteDescription: fmt.Sprintf("With `remove_on_destroy`, how long to wait for the machine to leave its "+
					"cluster before removing it anyway, as a Go duration. Defaults to %s.", defaultMachineLeaveTimeout),
			}),
		},
	}
}

// ConfigValidators implements resource.ResourceWithConfigValidators.
func (r *machineWaitResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("labels"),
			path.MatchRoot("machine_id"),
		),
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *machineWaitResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *machineWaitResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineWaitResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultMachineWaitTimeout)
	resp.Diagnostics.Append(diags...)

	labels := map[string]string{}

	if !plan.Labels.IsNull() {
		resp.Diagnostics.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	machineID := ""
	if !plan.MachineID.IsUnknown() {
		machineID = plan.MachineID.ValueString()
	}

	status, err := waitForMachine(ctx, r.data.state, machineID, labels, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Failed waiting for the machine to join Omni", err.Error())

		return
	}

	plan.MachineID = types.StringValue(status.Metadata().ID())
	plan.Connected = types.BoolValue(status.TypedSpec().Value.GetConnected())

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *machineWaitResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineWaitResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	status, err := safe.ReaderGetByID[*omni.MachineStatus](ctx, r.data.state, state.MachineID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read the Omni machine status", err)

		return
	}

	state.Connected = types.BoolValue(status.TypedSpec().Value.GetConnected())

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Every input forces replacement except the timeouts, so this
// only has to persist the plan.
func (r *machineWaitResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var (
		plan  machineWaitResourceModel
		state machineWaitResourceModel
	)

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	plan.MachineID = state.MachineID
	plan.Connected = state.Connected

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource. The machine belongs to whatever created it, so forgetting it
// is all there is to do.
func (r *machineWaitResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineWaitResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() || !state.RemoveOnDestroy.ValueBool() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultMachineLeaveTimeout)
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := waitForMachineToLeaveCluster(ctx, r.data.state, state.MachineID.ValueString(), timeout); err != nil {
		resp.Diagnostics.AddWarning("Removing a machine that has not left its cluster",
			fmt.Sprintf("The machine is removed from Omni anyway, but its cluster may not finish scaling down until "+
				"it is removed from it by hand: %s", err))
	}

	// Tearing the link down is how Omni removes a machine: it cleans up everything else that hangs
	// off the machine, and finishes on its own, so there is nothing to wait for here.
	if _, err := r.data.state.Teardown(ctx, siderolink.NewLink(state.MachineID.ValueString(), nil).Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to remove the machine from Omni", err)
	}
}

// waitForMachineToLeaveCluster waits until the machine is in no cluster.
//
// Omni takes a machine out of a cluster by resetting it, and only then destroys its ClusterMachine.
// A machine turned off before that is never reset, which leaves its cluster scaling down for good.
func waitForMachineToLeaveCluster(ctx context.Context, st cosistate.State, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var clusterMachine *omni.ClusterMachine

	err := waitUntil(ctx, st, []watchFunc{watchResource(omni.NewClusterMachine(id).Metadata())}, func(ctx context.Context) (bool, error) {
		current, err := safe.ReaderGetByID[*omni.ClusterMachine](ctx, st, id)

		switch {
		case cosistate.IsNotFoundError(err):
			return true, nil
		case err != nil && !isTransient(ctx, err) && ctx.Err() == nil:
			return false, err
		}

		if current != nil {
			clusterMachine = current
		}

		return false, nil
	})

	if err != nil && ctx.Err() != nil {
		cluster := "its cluster"

		if clusterMachine != nil {
			if name, ok := clusterMachine.Metadata().Labels().Get(omni.LabelCluster); ok {
				cluster = fmt.Sprintf("cluster %q", name)
			}
		}

		return fmt.Errorf("the machine was still in %s after %s", cluster, timeout)
	}

	return err
}

// waitForMachine waits until the machine selected by id or labels exists and is connected.
func waitForMachine(ctx context.Context, st cosistate.State, id string, labels map[string]string, timeout time.Duration) (*omni.MachineStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watch := watchKind(omni.NewMachineStatus("").Metadata(), cosistate.WatchWithLabelQuery(labelTerms(labels)...))
	if id != "" {
		watch = watchResource(omni.NewMachineStatus(id).Metadata())
	}

	var (
		status   *omni.MachineStatus
		lastSeen string
	)

	err := waitUntil(ctx, st, []watchFunc{watch}, func(ctx context.Context) (bool, error) {
		found, seen, err := findMachine(ctx, st, id, labels)
		if err != nil {
			return false, err
		}

		status, lastSeen = found, seen

		return status != nil, nil
	})

	switch {
	case err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("timed out after %s: %s", timeout, lastSeen)
	case err != nil:
		return nil, err
	default:
		return status, nil
	}
}

// findMachine looks the machine up once. It returns the machine when it is ready, or a description
// of what was seen instead when it is not, which is what a timeout reports.
func findMachine(ctx context.Context, st cosistate.State, id string, labels map[string]string) (*omni.MachineStatus, string, error) {
	if id != "" {
		status, err := safe.ReaderGetByID[*omni.MachineStatus](ctx, st, id)
		if err != nil {
			if cosistate.IsNotFoundError(err) || isTransient(ctx, err) {
				return nil, fmt.Sprintf("no machine %q has registered", id), nil
			}

			return nil, "", err
		}

		if !status.TypedSpec().Value.GetConnected() {
			return nil, fmt.Sprintf("machine %q has registered but is not connected", id), nil
		}

		return status, "", nil
	}

	list, err := safe.ReaderListAll[*omni.MachineStatus](ctx, st, cosistate.WithLabelQuery(labelTerms(labels)...))
	if err != nil {
		if isTransient(ctx, err) {
			return nil, "Omni could not be reached", nil
		}

		return nil, "", err
	}

	var (
		connected    []string
		match        *omni.MachineStatus
		disconnected int
	)

	for status := range list.All() {
		if !status.TypedSpec().Value.GetConnected() {
			disconnected++

			continue
		}

		match = status
		connected = append(connected, status.Metadata().ID())
	}

	switch {
	case len(connected) > 1:
		return nil, "", fmt.Errorf("%w %s: %s", errMachineAmbiguous, describeLabels(labels), strings.Join(connected, ", "))
	case match != nil:
		return match, "", nil
	case disconnected > 0:
		return nil, fmt.Sprintf("%d machine(s) with the labels %s registered, none of them connected", disconnected, describeLabels(labels)), nil
	default:
		return nil, fmt.Sprintf("no machine with the labels %s has registered", describeLabels(labels)), nil
	}
}

// isTransient reports an error worth retrying rather than failing on: the wait has a deadline, so
// a blip in the connection to Omni should not end it early. The deadline itself is not transient.
func isTransient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}

	return errors.Is(err, context.DeadlineExceeded) || grpcstatus.Code(err) == codes.Unavailable
}

// labelTerms selects resources carrying all the labels.
func labelTerms(labels map[string]string) []cosiresource.LabelQueryOption {
	terms := make([]cosiresource.LabelQueryOption, 0, len(labels))

	for _, key := range slices.Sorted(maps.Keys(labels)) {
		terms = append(terms, cosiresource.LabelEqual(key, labels[key]))
	}

	return terms
}

// describeLabels renders labels in a stable order for messages.
func describeLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))

	for _, key := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, key+"="+labels[key])
	}

	return "{" + strings.Join(parts, ", ") + "}"
}
