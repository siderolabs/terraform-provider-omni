// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                = (*defaultJoinTokenResource)(nil)
	_ resource.ResourceWithConfigure   = (*defaultJoinTokenResource)(nil)
	_ resource.ResourceWithImportState = (*defaultJoinTokenResource)(nil)
)

// defaultJoinTokenResourceModel maps the omni_default_join_token resource schema.
type defaultJoinTokenResourceModel struct {
	ID      types.String `tfsdk:"id"`
	TokenID types.String `tfsdk:"token_id"`
}

// defaultJoinTokenResource implements the omni_default_join_token resource.
type defaultJoinTokenResource struct {
	data *providerData
}

// NewDefaultJoinTokenResource returns a new omni_default_join_token resource.
func NewDefaultJoinTokenResource() resource.Resource {
	return &defaultJoinTokenResource{}
}

// Metadata implements resource.Resource.
func (r *defaultJoinTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_default_join_token"
}

// Schema implements resource.Resource.
func (r *defaultJoinTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Points the Omni instance's default join token at an `omni_join_token`. The default is what " +
			"installation media and machine join configurations use when they name no token of their own, so changing " +
			"it changes how every such machine joins from then on.\n\n" +
			"This is a singleton: the instance has exactly one default, which Omni creates at bootstrap and which " +
			"always exists. Adding this resource therefore adopts the existing setting rather than creating anything, " +
			"and destroying it leaves whatever token was last set in place, because clearing the default would stop " +
			"machines joining.\n\n" +
			"It is the Terraform equivalent of `omnictl jointoken make-default`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "Always `default`: the Omni instance has a single default join token, so the resource has " +
					"a fixed ID.",
			},
			"token_id": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				Description: "The join token to make the default, i.e. the `id` of an `omni_join_token`. It is the token " +
					"value itself, so treat it as a credential.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *defaultJoinTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
//
// Omni creates the default join token resource itself at bootstrap, so there is nothing to create
// here and the setting is adopted with an update, exactly as Update does.
func (r *defaultJoinTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan defaultJoinTokenResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	r.apply(ctx, &plan, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read implements resource.Resource.
func (r *defaultJoinTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state defaultJoinTokenResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	defaultToken, err := safe.ReaderGetByID[*siderolink.DefaultJoinToken](ctx, r.data.state, siderolink.DefaultJoinTokenID)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read the Omni default join token", err)

		return
	}

	state.ID = types.StringValue(siderolink.DefaultJoinTokenID)
	state.TokenID = types.StringValue(defaultToken.TypedSpec().Value.GetTokenId())

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
func (r *defaultJoinTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan defaultJoinTokenResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	r.apply(ctx, &plan, &resp.Diagnostics)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
//
// Deliberately a no-op. The default join token always exists and cannot be unset, and pointing it
// somewhere else on destroy would be a guess; leaving it alone means removing this resource from a
// configuration stops managing the default rather than changing how machines join.
func (r *defaultJoinTokenResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// ImportState implements resource.ResourceWithImportState.
//
// The import ID is ignored: there is only one default join token. `terraform import
// omni_default_join_token.this default` reads whatever the instance currently has.
func (r *defaultJoinTokenResource) ImportState(ctx context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), siderolink.DefaultJoinTokenID)...)
}

// apply points the default at the planned token and fills in the computed ID.
func (r *defaultJoinTokenResource) apply(ctx context.Context, plan *defaultJoinTokenResourceModel, diags *diag.Diagnostics) {
	tokenID := plan.TokenID.ValueString()

	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, siderolink.NewDefaultJoinToken().Metadata(),
		func(res *siderolink.DefaultJoinToken) error {
			res.TypedSpec().Value.TokenId = tokenID

			return nil
		})
	if err != nil {
		errToDiag(diags, "Failed to set the Omni default join token", err)

		return
	}

	plan.ID = types.StringValue(siderolink.DefaultJoinTokenID)
}
