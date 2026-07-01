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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

const (
	kubernetesManifestModeFull    = "full"
	kubernetesManifestModeOneTime = "one-time"
)

// kubernetesManifestModes maps the accepted mode strings to their spec enum values.
var kubernetesManifestModes = map[string]specs.KubernetesManifestGroupSpec_Mode{
	kubernetesManifestModeFull:    specs.KubernetesManifestGroupSpec_FULL,
	kubernetesManifestModeOneTime: specs.KubernetesManifestGroupSpec_ONE_TIME,
}

// kubernetesManifestModeNames maps the spec enum values back to the accepted mode strings.
var kubernetesManifestModeNames = map[specs.KubernetesManifestGroupSpec_Mode]string{
	specs.KubernetesManifestGroupSpec_FULL:     kubernetesManifestModeFull,
	specs.KubernetesManifestGroupSpec_ONE_TIME: kubernetesManifestModeOneTime,
}

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*kubernetesManifestResource)(nil)
	_ resource.ResourceWithConfigure   = (*kubernetesManifestResource)(nil)
	_ resource.ResourceWithImportState = (*kubernetesManifestResource)(nil)
)

// kubernetesManifestResourceModel maps the omni_kubernetes_manifest resource schema.
type kubernetesManifestResourceModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Mode    types.String `tfsdk:"mode"`
	Data    types.String `tfsdk:"data"`
	Cluster types.String `tfsdk:"cluster"`
	Weight  types.Int64  `tfsdk:"weight"`
}

// kubernetesManifestResource implements the omni_kubernetes_manifest resource.
type kubernetesManifestResource struct {
	data *providerData
}

// NewKubernetesManifestResource returns a new omni_kubernetes_manifest resource.
func NewKubernetesManifestResource() resource.Resource {
	return &kubernetesManifestResource{}
}

// Metadata implements resource.Resource.
func (r *kubernetesManifestResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_manifest"
}

// Schema implements resource.Resource.
func (r *kubernetesManifestResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a group of Kubernetes manifests (a KubernetesManifestGroup) that Omni applies to a cluster.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The generated manifest group ID, composed as `<zero-padded weight>-<cluster>-<name>`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The manifest group name, used together with the weight to form the resource ID. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"weight": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(defaultConfigPatchWeight),
				Description: "The manifest weight. Lower-weight groups are applied first. Defaults to 400. " +
					"Changing the weight changes the resource ID and replaces the manifest group.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"mode": schema.StringAttribute{
				Required:    true,
				Description: "How Omni applies the manifests. One of: full, one-time.",
				Validators: []validator.String{
					stringvalidator.OneOf(kubernetesManifestModeFull, kubernetesManifestModeOneTime),
				},
			},
			"data": schema.StringAttribute{
				Required:    true,
				Description: "The Kubernetes manifests to apply, as YAML (may contain multiple `---`-separated documents).",
			},
			"cluster": schema.StringAttribute{
				Required:    true,
				Description: "The cluster to apply the manifests to. Immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *kubernetesManifestResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *kubernetesManifestResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesManifestResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	id := configPatchID(plan.Weight.ValueInt64(), plan.Cluster.ValueString(), plan.Name.ValueString())

	manifest := omni.NewKubernetesManifestGroup(id)
	manifest.Metadata().Labels().Set(omni.LabelCluster, plan.Cluster.ValueString())
	manifest.Metadata().Annotations().Set(omni.KubernetesManifestName, plan.Name.ValueString())

	if err := r.applyManifest(plan, manifest); err != nil {
		errToDiag(&resp.Diagnostics, "Invalid Omni Kubernetes manifest", err)

		return
	}

	if err := r.data.state.Create(ctx, manifest); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni Kubernetes manifest", err)

		return
	}

	plan.ID = types.StringValue(id)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// applyManifest stores the mode and data on the manifest group.
func (r *kubernetesManifestResource) applyManifest(plan kubernetesManifestResourceModel, manifest *omni.KubernetesManifestGroup) error {
	manifest.TypedSpec().Value.Mode = kubernetesManifestModes[plan.Mode.ValueString()]

	return manifest.TypedSpec().Value.SetUncompressedData([]byte(plan.Data.ValueString()))
}

// Read implements resource.Resource.
func (r *kubernetesManifestResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesManifestResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	manifest, err := safe.ReaderGetByID[*omni.KubernetesManifestGroup](ctx, r.data.state, state.ID.ValueString())
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni Kubernetes manifest", err)

		return
	}

	buffer, err := manifest.TypedSpec().Value.GetUncompressedData()
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read Omni Kubernetes manifest data", err)

		return
	}

	data := string(buffer.Data())
	buffer.Free()

	weight, name := parseConfigPatchID(manifest.Metadata().ID())
	if annotation, ok := manifest.Metadata().Annotations().Get(omni.KubernetesManifestName); ok {
		name = annotation
	}

	state.ID = types.StringValue(manifest.Metadata().ID())
	state.Name = types.StringValue(name)
	state.Weight = types.Int64Value(weight)
	state.Mode = types.StringValue(kubernetesManifestModeNames[manifest.TypedSpec().Value.GetMode()])
	state.Data = types.StringValue(data)

	if cluster, ok := manifest.Metadata().Labels().Get(omni.LabelCluster); ok {
		state.Cluster = types.StringValue(cluster)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource. Only the mode and data are mutable.
func (r *kubernetesManifestResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan kubernetesManifestResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, omni.NewKubernetesManifestGroup(plan.ID.ValueString()).Metadata(),
		func(manifest *omni.KubernetesManifestGroup) error {
			return r.applyManifest(plan, manifest)
		})
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni Kubernetes manifest", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *kubernetesManifestResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesManifestResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	manifest := omni.NewKubernetesManifestGroup(state.ID.ValueString())

	if err := r.data.state.TeardownAndDestroy(ctx, manifest.Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni Kubernetes manifest", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Manifests are imported by their ID.
func (r *kubernetesManifestResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
