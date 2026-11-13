// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Ensure the resource satisfies the framework interfaces.
var (
	_ resource.Resource                   = (*joinTokenResource)(nil)
	_ resource.ResourceWithConfigure      = (*joinTokenResource)(nil)
	_ resource.ResourceWithImportState    = (*joinTokenResource)(nil)
	_ resource.ResourceWithValidateConfig = (*joinTokenResource)(nil)
)

// joinTokenResourceModel maps the omni_join_token resource schema.
type joinTokenResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Expiration types.String `tfsdk:"expiration"`
	State      types.String `tfsdk:"state"`
	UseCount   types.Int64  `tfsdk:"use_count"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
	Revoked    types.Bool   `tfsdk:"revoked"`
}

// joinTokenResource implements the omni_join_token resource.
type joinTokenResource struct {
	data *providerData
}

// NewJoinTokenResource returns a new omni_join_token resource.
func NewJoinTokenResource() resource.Resource {
	return &joinTokenResource{}
}

// Metadata implements resource.Resource.
func (r *joinTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_join_token"
}

// Schema implements resource.Resource.
func (r *joinTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Omni join token: the secret a machine presents to register itself with the instance.\n\n" +
			"The token is baked into installation media and into the machine join configuration, so " +
			"`omni_installation_media_preset` and the `omni_machine_join_config` data source both take one. Leaving them " +
			"without a token makes them track whichever token is the instance default, which `omni_default_join_token` " +
			"sets.\n\n" +
			"Destroying a token that machines are still using stops those machines from re-registering, so Omni records " +
			"how many have used it in `use_count`. Omni also refuses to destroy whichever token is currently the " +
			"instance default: point `omni_default_join_token` at another one first.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The token itself. It is both the resource ID and the secret a machine presents, so treat it " +
					"as a credential: anything it is written into, such as a kernel command line or a boot script, hands " +
					"the ability to join this instance to whoever can read it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "A human-readable name for the token, shown by `omnictl jointoken list` and the name " +
					"`omni_machine_join_config` looks a token up by. At most " + fmt.Sprintf("%d", joinTokenNameMaxLength) +
					" characters, and no control characters, which is what Omni accepts. Immutable: Omni fixes it when " +
					"the token is created.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, joinTokenNameMaxLength),
					stringvalidator.RegexMatches(
						joinTokenNamePattern,
						"must not contain control characters",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"revoked": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				Description: "Whether the token is revoked. Revoking stops new machines joining with it, without " +
					"destroying it, and can be undone by setting this back to `false`.",
			},
			"expiration": schema.StringAttribute{
				Optional: true,
				Description: "When the token stops being valid, as an RFC 3339 timestamp (e.g. `" + joinTokenExpirationExample +
					"`). Omni stores an absolute instant rather than a lifetime, so this is the very value it holds: the " +
					"same configuration always describes the same expiry, however long after it was written it is " +
					"applied. Changing it renews the token in place rather than replacing it, and removing it stops the " +
					"token expiring at all. Leave it unset for a token that does not expire.\n\n" +
					"It must be in the future when it is set or changed; an unchanged expiration that has since " +
					"passed is kept as is, so an expired token can still be revoked. To take a token out of service " +
					"without destroying it, set `revoked` instead, which is reversible.\n\n" +
					"For an expiry that rolls forward, drive it from the `time` provider rather than from " +
					"`timestamp()`, which changes on every plan and would renew the token on every apply.",
			},
			"state": schema.StringAttribute{
				Computed: true,
				Description: "The token's state as Omni reports it. " +
					describeOneOf(enumNames(specs.JoinTokenStatusSpec_State_name)),
			},
			"use_count": schema.Int64Attribute{
				Computed: true,
				Description: "How many machines have joined with this token. Destroying a token with a non-zero count " +
					"stops those machines from re-registering.",
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
				Description: "Whether this is the instance's default token, the one used by media and join configurations " +
					"that name no token. Set with `omni_default_join_token`.",
			},
		},
	}
}

// Configure implements resource.ResourceWithConfigure.
func (r *joinTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Create implements resource.Resource.
//
// Omni refuses a COSI create for a join token outright, directing callers to the management API, so
// the token is minted there and everything afterwards goes through the resource.
func (r *joinTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan joinTokenResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}

	expiration, ok := plannedJoinTokenExpiration(plan.Expiration, types.StringNull(), &resp.Diagnostics)
	if !ok {
		return
	}

	// The management API takes a lifetime rather than an instant, so the instant is turned back into
	// one here. Each end measures from its own reading of the clock, which can land the result a
	// fraction either side of what was asked for, so the exact instant is pinned below.
	var ttl time.Duration

	if expiration != nil {
		ttl = time.Until(expiration.AsTime())
	}

	token, err := r.data.client.Management().CreateJoinToken(ctx, plan.Name.ValueString(), ttl)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to create Omni join token", err)

		return
	}

	plan.ID = types.StringValue(token)

	// From here on the token exists, so a failure must not lose track of it: the token is a live
	// credential, and forgetting it would leave it active and unmanaged, with a retry minting another.
	// What is known so far is saved instead, which Terraform records as tainted and replaces on the
	// next apply. Until the call below succeeds, the token is unrevoked whatever was asked for.
	partial := plan
	partial.Revoked = types.BoolValue(false)

	// A token is created unrevoked, and with whatever expiration the lifetime above worked out to,
	// so anything the configuration asked for exactly takes a second call. The alternative is
	// leaving the configuration and the instance disagreeing.
	if expiration != nil || plan.Revoked.ValueBool() {
		if err = r.apply(ctx, token, expiration, plan.Revoked.ValueBool()); err != nil {
			errToDiag(&resp.Diagnostics, "Failed to configure the newly created Omni join token", err)
			r.savePartial(ctx, &partial, &resp.State, &resp.Diagnostics)

			return
		}

		partial.Revoked = plan.Revoked
	}

	if err = r.readInto(ctx, &plan); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read back the created Omni join token", err)
		r.savePartial(ctx, &partial, &resp.State, &resp.Diagnostics)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// savePartial records a token that was created but not fully configured, so that a failed create
// still leaves it in state. The fields Omni derives are not known yet, and an unknown value cannot
// be saved, so they are left null for the next refresh to fill in.
func (r *joinTokenResource) savePartial(ctx context.Context, model *joinTokenResourceModel, st *tfsdk.State, diags *diag.Diagnostics) {
	model.State = types.StringNull()
	model.UseCount = types.Int64Null()
	model.IsDefault = types.BoolNull()

	diags.Append(st.Set(ctx, model)...)
}

// Read implements resource.Resource.
func (r *joinTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state joinTokenResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	err := r.readInto(ctx, &state)
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)

			return
		}

		errToDiag(&resp.Diagnostics, "Failed to read Omni join token", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update implements resource.Resource.
//
// The name forces replacement, so the changes that reach here are a new expiration, which renews the
// token, and the revoked flag, both of which Omni accepts as a COSI update. Both are written from
// the configuration every time: an absolute expiration says what the stored value should be, so
// applying it twice is the same as applying it once, and whatever the instance holds is brought
// back in line.
func (r *joinTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state joinTokenResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Only a new expiration has to be in the future. One carried over unchanged may have passed in
	// the meantime, and that must not stand in the way of revoking the token it belongs to.
	expiration, ok := plannedJoinTokenExpiration(plan.Expiration, state.Expiration, &resp.Diagnostics)
	if !ok {
		return
	}

	plan.ID = state.ID

	if err := r.apply(ctx, plan.ID.ValueString(), expiration, plan.Revoked.ValueBool()); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to update Omni join token", err)

		return
	}

	if err := r.readInto(ctx, &plan); err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read back the updated Omni join token", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete implements resource.Resource.
func (r *joinTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state joinTokenResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.data.state.TeardownAndDestroy(ctx, siderolink.NewJoinToken(state.ID.ValueString()).Metadata()); err != nil {
		if cosistate.IsNotFoundError(err) {
			return
		}

		errToDiag(&resp.Diagnostics, "Failed to destroy Omni join token", err)

		return
	}
}

// ImportState implements resource.ResourceWithImportState. A join token is imported by its value,
// which is also its ID.
func (r *joinTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ValidateConfig implements resource.ResourceWithValidateConfig.
//
// Only the expiration's spelling is checked here. Whether it is still in the future is a question
// about the moment the change is applied, not the moment it is planned, so it belongs to the apply.
func (r *joinTokenResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config joinTokenResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if config.Expiration.IsNull() || config.Expiration.IsUnknown() {
		return
	}

	if _, err := parseJoinTokenExpiration(config.Expiration.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("expiration"), "Invalid expiration", err.Error())
	}
}

// plannedJoinTokenExpiration turns the `expiration` attribute into the timestamp Omni stores,
// reporting a bad spelling or a date already gone by through the diagnostics. A null attribute means
// the token does not expire, which Omni represents as no timestamp at all.
//
// prior is the expiration already in state. A planned value naming the same instant is not a new
// deadline, so it is let through even once it has passed: the token has simply expired, and other
// changes to it, such as revoking it, still have to be possible.
func plannedJoinTokenExpiration(planned, prior types.String, diags *diag.Diagnostics) (*timestamppb.Timestamp, bool) {
	if planned.IsNull() {
		return nil, true
	}

	expiration, err := parseJoinTokenExpiration(planned.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("expiration"), "Invalid expiration", err.Error())

		return nil, false
	}

	unchanged := false

	if !prior.IsNull() && !prior.IsUnknown() {
		if previous, err := parseJoinTokenExpiration(prior.ValueString()); err == nil && previous.Equal(expiration) {
			unchanged = true
		}
	}

	if !unchanged && !expiration.After(time.Now()) {
		diags.AddAttributeError(
			path.Root("expiration"),
			"Expiration has already passed",
			fmt.Sprintf(
				"%q is in the past, which would leave a token no machine can join with. Move it into the future, "+
					"or set `revoked = true` to take the token out of service without destroying it, which is "+
					"reversible.",
				planned.ValueString(),
			),
		)

		return nil, false
	}

	return timestamppb.New(expiration), true
}

// apply writes the two mutable fields of an existing token. Omni accepts both as a COSI update, so
// renewing and revoking are the same call.
func (r *joinTokenResource) apply(ctx context.Context, token string, expiration *timestamppb.Timestamp, revoked bool) error {
	_, err := safe.StateUpdateWithConflicts(ctx, r.data.state, siderolink.NewJoinToken(token).Metadata(),
		func(res *siderolink.JoinToken) error {
			res.TypedSpec().Value.ExpirationTime = expiration
			res.TypedSpec().Value.Revoked = revoked

			return nil
		})

	return err
}

// readInto refreshes the server-owned fields of the model.
//
// The token itself carries the name, expiration and revoked flag; the status carries what Omni
// derives from them, so both are read.
func (r *joinTokenResource) readInto(ctx context.Context, model *joinTokenResourceModel) error {
	id := model.ID.ValueString()

	token, err := safe.ReaderGetByID[*siderolink.JoinToken](ctx, r.data.state, id)
	if err != nil {
		return err
	}

	model.Name = types.StringValue(token.TypedSpec().Value.GetName())
	model.Revoked = types.BoolValue(token.TypedSpec().Value.GetRevoked())
	model.Expiration = reconcileTimestamp(model.Expiration, token.TypedSpec().Value.GetExpirationTime())

	status, err := safe.ReaderGetByID[*siderolink.JoinTokenStatus](ctx, r.data.state, id)
	if err != nil {
		// The status is produced by a controller, so a token can exist for a moment without one. The
		// token's own fields are already set, so report what is known rather than failing the read.
		if cosistate.IsNotFoundError(err) {
			model.State = types.StringNull()
			model.UseCount = types.Int64Null()
			model.IsDefault = types.BoolNull()

			return nil
		}

		return err
	}

	model.State = types.StringValue(enumName(specs.JoinTokenStatusSpec_State_name, int32(status.TypedSpec().Value.GetState())))
	model.UseCount = types.Int64Value(int64(status.TypedSpec().Value.GetUseCount()))
	model.IsDefault = types.BoolValue(status.TypedSpec().Value.GetIsDefault())

	return nil
}
