// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*kubernetesHealthCheckResource)(nil)
	_ resource.ResourceWithConfigure   = (*kubernetesHealthCheckResource)(nil)
	_ resource.ResourceWithImportState = (*kubernetesHealthCheckResource)(nil)
)

// kubernetesHealthCheckResourceModel maps the omni_kubernetes_healthcheck resource schema.
type kubernetesHealthCheckResourceModel struct {
	Name     types.String `tfsdk:"name"`
	Job      types.String `tfsdk:"job"`
	Interval types.String `tfsdk:"interval"`
	Cluster  types.String `tfsdk:"cluster"`
}

// kubernetesHealthCheckResource implements the omni_kubernetes_healthcheck resource.
type kubernetesHealthCheckResource struct {
	data *providerData
}

// NewKubernetesHealthCheckResource returns a new omni_kubernetes_healthcheck resource.
func NewKubernetesHealthCheckResource() resource.Resource {
	return &kubernetesHealthCheckResource{}
}

// Metadata implements resource.Resource.
func (r *kubernetesHealthCheckResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_healthcheck"
}

// Schema implements resource.Resource.
func (r *kubernetesHealthCheckResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an advanced Kubernetes healthcheck: a Kubernetes Job Omni runs to gate cluster upgrades.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The healthcheck name. This is the resource ID and is immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"job": schema.StringAttribute{
				Required:    true,
				Description: "The Kubernetes Job manifest Omni runs to perform the healthcheck, as YAML.",
			},
			"interval": schema.StringAttribute{
				Optional: true,
				Description: "How often Omni re-runs the healthcheck while holding an upgrade, as a Go duration string " +
					"(e.g. `30s`). Defaults to 30s when unset.",
			},
			"cluster": schema.StringAttribute{
				Required:    true,
				Description: "The cluster the healthcheck applies to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *kubernetesHealthCheckResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// applyHealthCheckModel populates a KubernetesHealthCheck resource from the model.
func (r *kubernetesHealthCheckResource) applyHealthCheckModel(plan kubernetesHealthCheckResourceModel, healthCheck *omni.KubernetesHealthCheck) error {
	healthCheck.TypedSpec().Value.Job = plan.Job.ValueString()
	healthCheck.TypedSpec().Value.Interval = nil

	if interval := plan.Interval.ValueString(); interval != "" {
		duration, err := time.ParseDuration(interval)
		if err != nil {
			return err
		}

		if duration > 0 {
			healthCheck.TypedSpec().Value.Interval = durationpb.New(duration)
		}
	}

	return nil
}

// Create implements resource.Resource.
func (r *kubernetesHealthCheckResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesHealthCheckResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	healthCheck := omni.NewKubernetesHealthCheck(plan.Name.ValueString())
	healthCheck.Metadata().Labels().Set(omni.LabelCluster, plan.Cluster.ValueString())

	if err := r.applyHealthCheckModel(plan, healthCheck); err != nil {
		errToDiag(&resp.Diagnostics, "Invalid Omni Kubernetes healthcheck", err)

		return
	}

	if err := r.data.state.Create(ctx, healthCheck); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni Kubernetes healthcheck", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *kubernetesHealthCheckResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesHealthCheckResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	healthCheck, err := safe.ReaderGetByID[*omni.KubernetesHealthCheck](ctx, r.data.state, state.Name.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni Kubernetes healthcheck", err)

		return
	}

	value := healthCheck.TypedSpec().Value

	state.Name = types.StringValue(healthCheck.Metadata().ID())
	state.Job = types.StringValue(value.GetJob())

	// interval is Optional (not Computed): when the user leaves it unset the server still reports its
	// own default (e.g. 30s). Materializing that default into state would create a persistent diff
	// against the null configuration, so the null/unknown value is preserved when the user did not
	// set an interval.
	if !state.Interval.IsNull() && !state.Interval.IsUnknown() {
		var interval time.Duration
		if value.GetInterval() != nil {
			interval = value.GetInterval().AsDuration()
		}

		state.Interval = reconcileDuration(state.Interval, interval)
	}

	if cluster, ok := healthCheck.Metadata().Labels().Get(omni.LabelCluster); ok {
		state.Cluster = types.StringValue(cluster)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
func (r *kubernetesHealthCheckResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan kubernetesHealthCheckResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewKubernetesHealthCheck(plan.Name.ValueString()).Metadata(),
		func(healthCheck *omni.KubernetesHealthCheck) error {
			return r.applyHealthCheckModel(plan, healthCheck)
		})
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni Kubernetes healthcheck", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *kubernetesHealthCheckResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesHealthCheckResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	healthCheck := omni.NewKubernetesHealthCheck(state.Name.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, healthCheck.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni Kubernetes healthcheck", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Healthchecks are imported by name.
func (r *kubernetesHealthCheckResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
