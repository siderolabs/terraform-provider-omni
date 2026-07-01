// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*clusterResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterResource)(nil)
	_ resource.ResourceWithImportState = (*clusterResource)(nil)
)

// clusterFeaturesModel maps the nested features block.
type clusterFeaturesModel struct {
	DiskEncryption              types.Bool `tfsdk:"disk_encryption"`
	EnableWorkloadProxy         types.Bool `tfsdk:"enable_workload_proxy"`
	UseEmbeddedDiscoveryService types.Bool `tfsdk:"use_embedded_discovery_service"`
}

// clusterResourceModel maps the omni_cluster resource schema.
type clusterResourceModel struct {
	Features          *clusterFeaturesModel `tfsdk:"features"`
	Name              types.String          `tfsdk:"name"`
	KubernetesVersion types.String          `tfsdk:"kubernetes_version"`
	TalosVersion      types.String          `tfsdk:"talos_version"`
	BackupInterval    types.String          `tfsdk:"backup_interval"`
}

// clusterResource implements the omni_cluster resource.
type clusterResource struct {
	data *providerData
}

// NewClusterResource returns a new omni_cluster resource.
func NewClusterResource() resource.Resource {
	return &clusterResource{}
}

// Metadata implements resource.Resource.
func (r *clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

// Schema implements resource.Resource.
func (r *clusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Omni cluster. Machine sets, machines and config patches are managed by their own resources " +
			"(`omni_machine_set`, `omni_machine_set_node`, `omni_config_patch`).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The cluster name. This is the resource ID and is immutable; changing it replaces the cluster.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kubernetes_version": schema.StringAttribute{
				Required:    true,
				Description: "The Kubernetes version to run, in semver format (e.g. `1.36.2`).",
			},
			"talos_version": schema.StringAttribute{
				Required:    true,
				Description: "The Talos version to run, in semver format (e.g. `1.13.5`).",
			},
			"backup_interval": schema.StringAttribute{
				Optional: true,
				Description: "Interval between automatic etcd backups, as a Go duration string (e.g. `1h`). " +
					"When unset or zero, etcd backups are disabled for this cluster.",
			},
			"features": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Cluster-wide feature flags.",
				Attributes: map[string]schema.Attribute{
					"disk_encryption": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(false),
						Description: "Enable disk encryption (KMS). Requires Talos >= 1.5.0.",
					},
					"enable_workload_proxy": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(false),
						Description: "Enable the workload service proxy.",
					},
					"use_embedded_discovery_service": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(false),
						Description: "Use the discovery service embedded in Omni instead of the public one.",
					},
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// applyClusterModel builds a Cluster resource spec from the model, validating the model first.
func (r *clusterResource) applyClusterModel(plan clusterResourceModel, cluster *omni.Cluster) error {
	kubernetesVersion := strings.TrimLeft(plan.KubernetesVersion.ValueString(), "v")
	talosVersion := strings.TrimLeft(plan.TalosVersion.ValueString(), "v")

	features := &specs.ClusterSpec_Features{}
	if plan.Features != nil {
		features.DiskEncryption = plan.Features.DiskEncryption.ValueBool()
		features.EnableWorkloadProxy = plan.Features.EnableWorkloadProxy.ValueBool()
		features.UseEmbeddedDiscoveryService = plan.Features.UseEmbeddedDiscoveryService.ValueBool()
	}

	validator := omni.ClusterValidator{
		ID:                plan.Name.ValueString(),
		KubernetesVersion: kubernetesVersion,
		TalosVersion:      talosVersion,
		EncryptionEnabled: features.DiskEncryption,
	}

	if err := validator.Validate(); err != nil {
		return err
	}

	cluster.TypedSpec().Value.KubernetesVersion = kubernetesVersion
	cluster.TypedSpec().Value.TalosVersion = talosVersion
	cluster.TypedSpec().Value.Features = features

	if interval := plan.BackupInterval.ValueString(); interval != "" {
		duration, err := time.ParseDuration(interval)
		if err != nil {
			return err
		}

		if duration > 0 {
			cluster.TypedSpec().Value.BackupConfiguration = &specs.EtcdBackupConf{
				Interval: durationpb.New(duration),
				Enabled:  true,
			}
		} else {
			cluster.TypedSpec().Value.BackupConfiguration = nil
		}
	} else {
		cluster.TypedSpec().Value.BackupConfiguration = nil
	}

	return nil
}

// Create implements resource.Resource.
func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	cluster := omni.NewCluster(plan.Name.ValueString())

	if err := r.applyClusterModel(plan, cluster); err != nil {
		errToDiag(&resp.Diagnostics, "Invalid Omni cluster", err)

		return
	}

	if err := r.data.state.Create(ctx, cluster); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni cluster", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	cluster, err := safe.ReaderGetByID[*omni.Cluster](ctx, r.data.state, state.Name.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni cluster", err)

		return
	}

	r.clusterToModel(cluster, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clusterResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewCluster(plan.Name.ValueString()).Metadata(),
		func(cluster *omni.Cluster) error {
			return r.applyClusterModel(plan, cluster)
		})
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni cluster", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clusterResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	cluster := omni.NewCluster(state.Name.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, cluster.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni cluster", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Clusters are imported by name.
func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// clusterToModel populates the model from a Cluster resource read from Omni.
func (r *clusterResource) clusterToModel(cluster *omni.Cluster, model *clusterResourceModel) {
	value := cluster.TypedSpec().Value

	model.Name = types.StringValue(cluster.Metadata().ID())
	model.KubernetesVersion = types.StringValue(value.GetKubernetesVersion())
	model.TalosVersion = types.StringValue(value.GetTalosVersion())

	features := value.GetFeatures()
	anyFeatureEnabled := features != nil &&
		(features.GetDiskEncryption() || features.GetEnableWorkloadProxy() || features.GetUseEmbeddedDiscoveryService())

	// Preserve the presence of the features block: an all-false features block is only materialized
	// when it (or a prior state) already had one, to avoid a null-vs-empty-object diff.
	if anyFeatureEnabled || model.Features != nil {
		model.Features = &clusterFeaturesModel{
			DiskEncryption:              types.BoolValue(features.GetDiskEncryption()),
			EnableWorkloadProxy:         types.BoolValue(features.GetEnableWorkloadProxy()),
			UseEmbeddedDiscoveryService: types.BoolValue(features.GetUseEmbeddedDiscoveryService()),
		}
	}

	var interval time.Duration
	if backup := value.GetBackupConfiguration(); backup != nil && backup.GetEnabled() && backup.GetInterval() != nil {
		interval = backup.GetInterval().AsDuration()
	}

	model.BackupInterval = reconcileDuration(model.BackupInterval, interval)
}
