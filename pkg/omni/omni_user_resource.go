// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/access/role"
	"github.com/siderolabs/omni/client/pkg/omni/resources/auth"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*userResource)(nil)
	_ resource.ResourceWithConfigure   = (*userResource)(nil)
	_ resource.ResourceWithImportState = (*userResource)(nil)
)

// userResourceModel maps the omni_user resource schema.
type userResourceModel struct {
	ID    types.String `tfsdk:"id"`
	Email types.String `tfsdk:"email"`
	Role  types.String `tfsdk:"role"`
}

// userResource implements the omni_user resource.
type userResource struct {
	data *providerData
}

// NewUserResource returns a new omni_user resource.
func NewUserResource() resource.Resource {
	return &userResource{}
}

// Metadata implements resource.Resource.
func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// Schema implements resource.Resource.
func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Omni user and its associated identity (keyed by email).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The generated user ID (UUID).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"email": schema.StringAttribute{
				Required:    true,
				Description: "The email of the user. This is the identity ID and is immutable; changing it replaces the user.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				Required:    true,
				Description: "The role of the user. One of: None, Reader, Operator, Admin, InfraProvider.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(role.None),
						string(role.Reader),
						string(role.Operator),
						string(role.Admin),
						string(role.InfraProvider),
					),
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	email := strings.ToLower(plan.Email.ValueString())

	userID, err := r.data.client.Management().CreateUser(ctx, email, plan.Role.ValueString())
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni user", err)

		return
	}

	plan.ID = types.StringValue(userID)
	plan.Email = types.StringValue(email)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	email := strings.ToLower(state.Email.ValueString())

	identity, err := safe.ReaderGetByID[*auth.Identity](ctx, r.data.state, email)
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni identity", err)

		return
	}

	userID := identity.TypedSpec().Value.GetUserId()

	user, err := safe.ReaderGetByID[*auth.User](ctx, r.data.state, userID)
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni user", err)

		return
	}

	state.ID = types.StringValue(userID)
	state.Email = types.StringValue(email)
	state.Role = types.StringValue(user.TypedSpec().Value.GetRole())

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan userResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	email := strings.ToLower(plan.Email.ValueString())

	if err := r.data.client.Management().UpdateUser(ctx, email, plan.Role.ValueString()); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni user", err)

		return
	}

	plan.Email = types.StringValue(email)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	email := strings.ToLower(state.Email.ValueString())

	if err := r.data.client.Management().DestroyUser(ctx, email); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni user", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. Users are imported by email.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("email"), req, resp)
}
