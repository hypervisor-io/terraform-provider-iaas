package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Ensure IaasProvider satisfies the provider.Provider interface.
var _ provider.Provider = &IaasProvider{}

// IaasProvider is the top-level provider implementation.
type IaasProvider struct {
	version string
}

// New returns a constructor for IaasProvider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &IaasProvider{version: version}
	}
}

// Metadata sets the provider type name and version.
func (p *IaasProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "iaas"
	resp.Version = p.version
}

// Schema returns an empty provider schema (attributes added in later tasks).
func (p *IaasProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{}
}

// Configure is a stub; client configuration is wired in a later task.
func (p *IaasProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

// Resources returns the list of resources provided (populated in later tasks).
func (p *IaasProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

// DataSources returns the list of data sources provided (populated in later tasks).
func (p *IaasProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
