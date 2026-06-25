// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/auth"
)

// Ensure the data source satisfies the framework interfaces.
var (
	_ datasource.DataSource              = (*userDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*userDataSource)(nil)
)

// userDataSourceModel maps the omni_user data source schema.
type userDataSourceModel struct {
	ID    types.String `tfsdk:"id"`
	Email types.String `tfsdk:"email"`
	Role  types.String `tfsdk:"role"`
}

// userDataSource implements the omni_user data source.
type userDataSource struct {
	data *providerData
}

// NewUserDataSource returns a new omni_user data source.
func NewUserDataSource() datasource.DataSource {
	return &userDataSource{}
}

// Metadata implements datasource.DataSource.
func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// Schema implements datasource.DataSource.
func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Omni user by email.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The user ID (UUID).",
			},
			"email": schema.StringAttribute{
				Required:    true,
				Description: "The email of the user to look up.",
			},
			"role": schema.StringAttribute{
				Computed:    true,
				Description: "The role of the user.",
			},
		},
	}
}

// Configure implements datasource.DataSourceWithConfigure.
func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFromResource(req.ProviderData, &resp.Diagnostics)
}

// Read implements datasource.DataSource.
func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config userDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	email := strings.ToLower(config.Email.ValueString())

	identity, err := safe.ReaderGetByID[*auth.Identity](ctx, d.data.state, email)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read Omni identity", err)

		return
	}

	userID := identity.TypedSpec().Value.GetUserId()

	user, err := safe.ReaderGetByID[*auth.User](ctx, d.data.state, userID)
	if err != nil {
		errToDiag(&resp.Diagnostics, "Failed to read Omni user", err)

		return
	}

	config.ID = types.StringValue(userID)
	config.Email = types.StringValue(email)
	config.Role = types.StringValue(user.TypedSpec().Value.GetRole())

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
