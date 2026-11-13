// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	cosiresource "github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource              = (*clusterWaitResource)(nil)
	_ resource.ResourceWithConfigure = (*clusterWaitResource)(nil)
)

// defaultClusterWaitTimeout covers installing Talos, bootstrapping etcd and bringing Kubernetes up.
const defaultClusterWaitTimeout = 30 * time.Minute

// clusterWaitResourceModel maps the omni_cluster_wait resource schema.
type clusterWaitResourceModel struct {
	Triggers        types.Map      `tfsdk:"triggers"`
	MachineIDs      types.Set      `tfsdk:"machine_ids"`
	Cluster         types.String   `tfsdk:"cluster"`
	Phase           types.String   `tfsdk:"phase"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
	MachinesTotal   types.Int64    `tfsdk:"machines_total"`
	MachinesHealthy types.Int64    `tfsdk:"machines_healthy"`
	Ready           types.Bool     `tfsdk:"ready"`
}

// clusterWaitResource implements the omni_cluster_wait resource.
type clusterWaitResource struct {
	data *providerData
}

// NewClusterWaitResource returns a new omni_cluster_wait resource.
func NewClusterWaitResource() resource.Resource {
	return &clusterWaitResource{}
}

// Metadata implements resource.Resource.
func (r *clusterWaitResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_wait"
}

// Schema implements resource.Resource.
func (r *clusterWaitResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Waits for an Omni cluster to become ready: every machine set running and ready, every requested " +
			"machine allocated and healthy, the control plane ready, the Kubernetes API reachable, and the cluster " +
			"running the Talos and Kubernetes versions it is configured with, so that a wait after an upgrade only " +
			"ends once the upgrade is done. It is what the Omni UI shows as a cluster that is up.\n\n" +
			"Omni brings a cluster up asynchronously, so `omni_cluster` and `omni_machine_set_node` are done as soon " +
			"as Omni has accepted them. Depend on this resource from anything that needs the cluster working, such as " +
			"a Kubernetes provider pointed at it, and make it depend on the machine set nodes so it starts waiting " +
			"once they all exist.\n\n" +
			"Only creating this resource waits. Refreshing it reads the cluster status once and never blocks a plan. " +
			"Changing `machine_ids` waits again; to wait again after any other change, the cluster upgraded for " +
			"instance, put something that changes with it in `triggers`. Destroying it does nothing to the cluster.",
		Attributes: map[string]schema.Attribute{
			"cluster": schema.StringAttribute{
				Required:    true,
				Description: "The name of the cluster to wait for.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"machine_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "The UUIDs of the machines the cluster should have. When set, the cluster is only ready " +
					"once its machines are exactly these, each of them running and ready, and changing them waits " +
					"again. Set it whenever machines are " +
					"removed: Terraform may start waiting before Omni has begun taking the removed machines out, and the " +
					"cluster still reports ready with them in it, so without this the wait could end before the scale " +
					"down has even started.",
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
			"triggers": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Arbitrary values that, when changed, make Terraform wait for the cluster again, for " +
					"instance its Talos and Kubernetes versions.",
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"ready": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the cluster is ready. Always true right after the wait; a later refresh reports " +
					"what Omni says at that time.",
			},
			"phase": schema.StringAttribute{
				Computed:    true,
				Description: "The cluster phase Omni reports, such as `Running` or `ScalingUp`.",
			},
			"machines_total": schema.Int64Attribute{
				Computed:    true,
				Description: "How many machines the cluster has.",
			},
			"machines_healthy": schema.Int64Attribute{
				Computed:    true,
				Description: "How many of the cluster's machines are healthy.",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create:            true,
				CreateDescription: fmt.Sprintf("How long to wait for the cluster, as a Go duration. Defaults to %s.", defaultClusterWaitTimeout),
			}),
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *clusterWaitResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *clusterWaitResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterWaitResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultClusterWaitTimeout)
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	var machineIDs []string

	if !plan.MachineIDs.IsNull() {
		machineIDs = []string{}

		resp.Diagnostics.Append(plan.MachineIDs.ElementsAs(ctx, &machineIDs, false)...)

		if resp.Diagnostics.HasError() {
			return
		}
	}

	status, err := waitForCluster(ctx, r.data.state, plan.Cluster.ValueString(), machineIDs, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Failed waiting for the Omni cluster to become ready", err.Error())

		return
	}

	plan.setStatus(status)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *clusterWaitResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterWaitResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	status, err := safe.ReaderGetByID[*omni.ClusterStatus](ctx, r.data.state, state.Cluster.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read the Omni cluster status", err)

		return
	}

	state.setStatus(status)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Every input forces replacement except the timeouts, so this
// only has to persist the plan.
func (r *clusterWaitResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var (
		plan  clusterWaitResourceModel
		state clusterWaitResourceModel
	)

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	plan.Ready = state.Ready
	plan.Phase = state.Phase
	plan.MachinesTotal = state.MachinesTotal
	plan.MachinesHealthy = state.MachinesHealthy

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource. Nothing was created, so there is nothing to remove.
func (r *clusterWaitResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (m *clusterWaitResourceModel) setStatus(status *omni.ClusterStatus) {
	spec := status.TypedSpec().Value

	m.Ready = types.BoolValue(spec.GetReady())
	m.Phase = types.StringValue(enumDisplayName(spec.GetPhase().String()))
	m.MachinesTotal = types.Int64Value(int64(spec.GetMachines().GetTotal()))
	m.MachinesHealthy = types.Int64Value(int64(spec.GetMachines().GetHealthy()))
}

// waitForCluster waits until the cluster is ready, has converged on its desired Talos and
// Kubernetes versions, and when machineIDs is not nil, until its machines are exactly those, each
// running and ready.
func waitForCluster(ctx context.Context, st cosistate.State, cluster string, machineIDs []string, timeout time.Duration) (*omni.ClusterStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watches := []watchFunc{
		watchResource(omni.NewClusterStatus(cluster).Metadata()),
		watchResource(omni.NewCluster(cluster).Metadata()),
		watchResource(omni.NewTalosUpgradeStatus(cluster).Metadata()),
		watchResource(omni.NewKubernetesUpgradeStatus(cluster).Metadata()),
	}

	if machineIDs != nil {
		watches = append(watches,
			watchKind(omni.NewClusterMachine("").Metadata(),
				cosistate.WatchWithLabelQuery(cosiresource.LabelEqual(omni.LabelCluster, cluster))),
			watchKind(omni.NewClusterMachineStatus("").Metadata(),
				cosistate.WatchWithLabelQuery(cosiresource.LabelEqual(omni.LabelCluster, cluster))),
		)
	}

	var (
		status   *omni.ClusterStatus
		lastSeen = "the cluster status was never read"
	)

	err := waitUntil(ctx, st, watches, func(ctx context.Context) (bool, error) {
		current, notReady, err := checkCluster(ctx, st, cluster, machineIDs)

		switch {
		case err == nil:
		case isTransient(ctx, err):
			lastSeen = "Omni could not be reached"

			return false, nil //nolint:nilerr // A blip in the connection is retried until the deadline.
		default:
			return false, err
		}

		if len(notReady) > 0 {
			lastSeen = strings.Join(notReady, "; ")

			return false, nil
		}

		status = current

		return true, nil
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

// checkCluster reads everything the wait depends on and lists what keeps the cluster from being
// done, nothing when it is. Any error reading Omni is returned, so that a failed read is never
// taken for a passed check.
func checkCluster(ctx context.Context, st cosistate.State, cluster string, machineIDs []string) (*omni.ClusterStatus, []string, error) {
	status, err := safe.ReaderGetByID[*omni.ClusterStatus](ctx, st, cluster)

	switch {
	case err == nil:
	case cosistate.IsNotFoundError(err):
		// Omni creates the status shortly after the cluster, so this is the start of the wait.
		return nil, []string{fmt.Sprintf("cluster %q has no status yet", cluster)}, nil
	default:
		return nil, nil, err
	}

	var notReady []string

	if reasons := clusterNotReady(status.TypedSpec().Value); len(reasons) > 0 {
		notReady = append(notReady, "the cluster is not ready: "+strings.Join(reasons, ", "))
	}

	upgrade, err := clusterUpgradeNotDone(ctx, st, cluster)
	if err != nil {
		return nil, nil, err
	}

	notReady = append(notReady, upgrade...)

	if machineIDs != nil {
		machines, err := clusterMachinesNotReady(ctx, st, cluster, status.TypedSpec().Value, machineIDs)
		if err != nil {
			return nil, nil, err
		}

		notReady = append(notReady, machines...)
	}

	return status, notReady, nil
}

// clusterUpgradeNotDone lists what keeps the cluster from running the Talos and Kubernetes versions
// it is configured with.
//
// Changing the versions of an `omni_cluster` only updates its spec: until Omni has picked the change
// up, the cluster status still describes the cluster as it was, ready and running, so that alone
// would end a wait triggered by an upgrade before the upgrade has even begun.
func clusterUpgradeNotDone(ctx context.Context, st cosistate.State, cluster string) ([]string, error) {
	desired, err := safe.ReaderGetByID[*omni.Cluster](ctx, st, cluster)

	switch {
	case err == nil:
	case cosistate.IsNotFoundError(err):
		return []string{fmt.Sprintf("cluster %q does not exist", cluster)}, nil
	default:
		return nil, err
	}

	talos, err := safe.ReaderGetByID[*omni.TalosUpgradeStatus](ctx, st, cluster)
	if err != nil && !cosistate.IsNotFoundError(err) {
		return nil, err
	}

	kubernetes, err := safe.ReaderGetByID[*omni.KubernetesUpgradeStatus](ctx, st, cluster)
	if err != nil && !cosistate.IsNotFoundError(err) {
		return nil, err
	}

	var talosSpec *specs.TalosUpgradeStatusSpec
	if talos != nil {
		talosSpec = talos.TypedSpec().Value
	}

	var kubernetesSpec *specs.KubernetesUpgradeStatusSpec
	if kubernetes != nil {
		kubernetesSpec = kubernetes.TypedSpec().Value
	}

	return upgradeNotDone(desired.TypedSpec().Value, talosSpec, kubernetesSpec), nil
}

// upgradeNotDone compares the versions a cluster is configured with to what the upgrade statuses
// report, a nil status meaning that Omni has not reported one yet.
func upgradeNotDone(desired *specs.ClusterSpec, talos *specs.TalosUpgradeStatusSpec, kubernetes *specs.KubernetesUpgradeStatusSpec) []string {
	var reasons []string

	switch {
	case talos == nil:
		reasons = append(reasons, "Talos upgrade status is not reported yet")
	case talos.GetPhase() != specs.TalosUpgradeStatusSpec_Done:
		reasons = append(reasons, upgradeInProgress("Talos", enumDisplayName(talos.GetPhase().String()), talos.GetCurrentUpgradeVersion(), talos.GetError()))
	case !sameVersion(talos.GetLastUpgradeVersion(), desired.GetTalosVersion()):
		reasons = append(reasons, fmt.Sprintf("Talos is at %s, not yet at %s", talos.GetLastUpgradeVersion(), desired.GetTalosVersion()))
	}

	switch {
	case kubernetes == nil:
		reasons = append(reasons, "Kubernetes upgrade status is not reported yet")
	case kubernetes.GetPhase() != specs.KubernetesUpgradeStatusSpec_Done:
		reasons = append(reasons, upgradeInProgress("Kubernetes", enumDisplayName(kubernetes.GetPhase().String()), kubernetes.GetCurrentUpgradeVersion(), kubernetes.GetError()))
	case !sameVersion(kubernetes.GetLastUpgradeVersion(), desired.GetKubernetesVersion()):
		reasons = append(reasons, fmt.Sprintf("Kubernetes is at %s, not yet at %s", kubernetes.GetLastUpgradeVersion(), desired.GetKubernetesVersion()))
	}

	return reasons
}

func upgradeInProgress(what, phase, version, errMessage string) string {
	reason := fmt.Sprintf("%s upgrade phase is %s", what, phase)

	if version != "" {
		reason += " to " + version
	}

	if errMessage != "" {
		reason += ": " + errMessage
	}

	return reason
}

// sameVersion compares two versions, with or without their leading `v`.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// clusterMachinesNotReady lists what keeps the cluster's machines from being exactly the expected
// ones, each running and ready.
//
// While machines are added or removed, the cluster status can still describe the previous
// membership, so its counts must match the expected machines exactly, and each of them must be
// running and ready on its own account.
func clusterMachinesNotReady(ctx context.Context, st cosistate.State, cluster string, spec *specs.ClusterStatusSpec, expected []string) ([]string, error) {
	reasons, err := clusterMachinesMismatch(ctx, st, cluster, expected)
	if err != nil {
		return nil, err
	}

	want := uint32(len(expected))
	machines := spec.GetMachines()

	if machines.GetTotal() != want {
		reasons = append(reasons, fmt.Sprintf("the cluster reports %d machines, not %d", machines.GetTotal(), want))
	}

	if machines.GetHealthy() != want {
		reasons = append(reasons, fmt.Sprintf("the cluster reports %d healthy machines, not %d", machines.GetHealthy(), want))
	}

	list, err := safe.ReaderListAll[*omni.ClusterMachineStatus](ctx, st, cosistate.WithLabelQuery(cosiresource.LabelEqual(omni.LabelCluster, cluster)))
	if err != nil {
		return nil, err
	}

	statuses := map[string]*specs.ClusterMachineStatusSpec{}

	for machine := range list.All() {
		statuses[machine.Metadata().ID()] = machine.TypedSpec().Value
	}

	var notRunning []string

	for _, id := range slices.Sorted(slices.Values(expected)) {
		machine, ok := statuses[id]

		switch {
		case !ok:
			notRunning = append(notRunning, id+" (no status yet)")
		case machine.GetStage() != specs.ClusterMachineStatusSpec_RUNNING:
			notRunning = append(notRunning, fmt.Sprintf("%s (%s)", id, enumDisplayName(machine.GetStage().String())))
		case !machine.GetReady():
			notRunning = append(notRunning, id+" (not ready)")
		}
	}

	if len(notRunning) > 0 {
		reasons = append(reasons, "machines not running and ready: "+strings.Join(notRunning, ", "))
	}

	return reasons, nil
}

// clusterNotReady lists what keeps a cluster from being ready, nothing when it is.
//
// Ready alone is not enough. Omni can report a cluster ready while a node has just been requested
// and not yet allocated, so the machine counts are checked against what was requested, and a
// cluster without machines is not considered up at all.
func clusterNotReady(spec *specs.ClusterStatusSpec) []string {
	var reasons []string

	if spec.GetPhase() != specs.ClusterStatusSpec_RUNNING {
		reasons = append(reasons, "phase is "+enumDisplayName(spec.GetPhase().String()))
	}

	if !spec.GetReady() {
		reasons = append(reasons, "machine sets are not ready")
	}

	if !spec.GetControlplaneReady() {
		reasons = append(reasons, "control plane is not ready")
	}

	if !spec.GetKubernetesAPIReady() {
		reasons = append(reasons, "Kubernetes API is not ready")
	}

	machines := spec.GetMachines()

	switch {
	case machines.GetTotal() == 0:
		reasons = append(reasons, "no machines are allocated")
	case machines.GetTotal() < machines.GetRequested():
		reasons = append(reasons, fmt.Sprintf("%d of %d requested machines are allocated", machines.GetTotal(), machines.GetRequested()))
	}

	if machines.GetHealthy() < machines.GetTotal() {
		reasons = append(reasons, fmt.Sprintf("%d of %d machines are healthy", machines.GetHealthy(), machines.GetTotal()))
	}

	return reasons
}

// clusterMachinesMismatch describes how the cluster's machines differ from the expected ones,
// nothing when they are the same. A machine being torn down is still in the cluster until Omni has
// finished taking it out.
func clusterMachinesMismatch(ctx context.Context, st cosistate.State, cluster string, expected []string) ([]string, error) {
	list, err := safe.ReaderListAll[*omni.ClusterMachine](ctx, st, cosistate.WithLabelQuery(cosiresource.LabelEqual(omni.LabelCluster, cluster)))
	if err != nil {
		return nil, err
	}

	actual := map[string]struct{}{}

	for machine := range list.All() {
		actual[machine.Metadata().ID()] = struct{}{}
	}

	var missing, extra []string

	for _, id := range expected {
		if _, ok := actual[id]; !ok {
			missing = append(missing, id)
		}

		delete(actual, id)
	}

	for id := range actual {
		extra = append(extra, id)
	}

	var reasons []string

	if len(missing) > 0 {
		slices.Sort(missing)
		reasons = append(reasons, "not in the cluster yet: "+strings.Join(missing, ", "))
	}

	if len(extra) > 0 {
		slices.Sort(extra)
		reasons = append(reasons, "still in the cluster: "+strings.Join(extra, ", "))
	}

	return reasons, nil
}
