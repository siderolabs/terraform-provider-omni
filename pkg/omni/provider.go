// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package omni implements the Terraform provider for Siderolabs Omni.
package omni

import (
	"context"
	"os"

	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/go-api-signature/pkg/serviceaccount"
	omniclient "github.com/siderolabs/omni/client/pkg/client"
)

// Ensure OmniProvider satisfies the provider.Provider interface.
var _ provider.Provider = (*OmniProvider)(nil)

// OmniProvider is the Terraform provider for Omni.
type OmniProvider struct {
	version string
}

// New returns a factory for the Omni provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &OmniProvider{version: version}
	}
}

// providerData is shared with resources and data sources via Configure.
type providerData struct {
	client *omniclient.Client
	state  cosistate.State
}

// OmniProviderModel maps the provider configuration schema.
type OmniProviderModel struct {
	Endpoint              types.String `tfsdk:"endpoint"`
	ServiceAccountKey     types.String `tfsdk:"service_account_key"`
	InsecureSkipTLSVerify types.Bool   `tfsdk:"insecure_skip_tls_verify"`
}

// Metadata implements provider.Provider.
func (p *OmniProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "omni"
	resp.Version = p.version
}

// Schema implements provider.Provider.
func (p *OmniProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Omni provider manages resources on a Siderolabs Omni instance via its COSI resource and management APIs.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "Omni API endpoint, e.g. `https://instance.omni.siderolabs.io`. May also be set via the `OMNI_ENDPOINT` environment variable.",
			},
			"service_account_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Base64-encoded Omni service account key. May also be set via the `OMNI_SERVICE_ACCOUNT_KEY` environment variable.",
			},
			"insecure_skip_tls_verify": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip TLS certificate verification when connecting to the Omni endpoint. Not recommended outside of development.",
			},
		},
	}
}

// Configure implements provider.Provider.
func (p *OmniProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config OmniProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := config.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv("OMNI_ENDPOINT")
	}

	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing Omni endpoint",
			"The provider requires an Omni endpoint. Set the `endpoint` attribute or the OMNI_ENDPOINT environment variable.",
		)

		return
	}

	serviceAccountKey := config.ServiceAccountKey.ValueString()
	if serviceAccountKey == "" {
		if _, valueBase64 := serviceaccount.GetFromEnv(); valueBase64 != "" {
			serviceAccountKey = valueBase64
		}
	}

	opts := []omniclient.Option{}

	if serviceAccountKey != "" {
		opts = append(opts, omniclient.WithServiceAccount(serviceAccountKey))
	}

	if config.InsecureSkipTLSVerify.ValueBool() {
		opts = append(opts, omniclient.WithInsecureSkipTLSVerify(true))
	}

	client, err := omniclient.New(endpoint, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Omni client", err.Error())

		return
	}

	data := &providerData{
		client: client,
		state:  newManagedState(client.Omni().State()),
	}

	resp.ResourceData = data
	resp.DataSourceData = data
}

// Resources implements provider.Provider.
func (p *OmniProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewUserResource,
		NewClusterResource,
		NewMachineSetResource,
		NewMachineSetNodeResource,
		NewConfigPatchResource,
		NewMachineExtensionsResource,
		NewKubernetesManifestResource,
		NewKubernetesHealthCheckResource,
	}
}

// DataSources implements provider.Provider.
func (p *OmniProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewUserDataSource,
	}
}
