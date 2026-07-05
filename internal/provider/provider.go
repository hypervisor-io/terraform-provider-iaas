package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/datasources"
	"github.com/iaas/terraform-provider-iaas/internal/resources"
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

// Schema returns the provider configuration schema.
//
// Four optional attributes are defined:
//   - endpoint: API base URL (including /api path).
//   - token: Bearer token for authentication (sensitive).
//   - request_timeout: HTTP timeout in seconds.
//   - insecure: Skip TLS certificate verification.
func (p *IaasProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The IaaS provider manages virtual machine infrastructure via the IaaS REST API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				Description: "Base URL of the IaaS API including the /api path prefix " +
					"(e.g. https://panel.example.com/api). " +
					"May also be set via the IAAS_API_ENDPOINT environment variable. " +
					"Required: either this attribute or IAAS_API_ENDPOINT must be set.",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Bearer token used to authenticate every API request. " +
					"May also be set via the IAAS_API_TOKEN environment variable. " +
					"Required: either this attribute or IAAS_API_TOKEN must be set.",
			},
			"request_timeout": schema.Int64Attribute{
				Optional: true,
				Description: "HTTP request timeout in seconds. " +
					"Defaults to 30 seconds when not set or set to 0.",
			},
			"insecure": schema.BoolAttribute{
				Optional: true,
				Description: "When true, TLS certificate verification is skipped. " +
					"Useful for self-signed certificates on staging environments. " +
					"Do not enable in production.",
			},
		},
	}
}

// providerModel maps the provider HCL configuration block.
type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	Token          types.String `tfsdk:"token"`
	RequestTimeout types.Int64  `tfsdk:"request_timeout"`
	Insecure       types.Bool   `tfsdk:"insecure"`
}

// Configure decodes the provider configuration and builds the API client.
// The client is stored in both resp.ResourceData and resp.DataSourceData so
// that resources and data sources can retrieve it from their own Configure
// method.
func (p *IaasProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Guard: values that are unknown at plan time cannot be used.
	if cfg.Endpoint.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Unknown IaaS API Endpoint",
			"The provider cannot be configured with an unknown value for endpoint. "+
				"Either set the value statically in configuration, or ensure the value is known before applying.",
		)
	}
	if cfg.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Unknown IaaS API Token",
			"The provider cannot be configured with an unknown value for token. "+
				"Either set the value statically in configuration, or ensure the value is known before applying.",
		)
	}
	if cfg.RequestTimeout.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("request_timeout"),
			"Unknown request_timeout",
			"The provider cannot be configured with an unknown value for request_timeout.",
		)
	}
	if cfg.Insecure.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("insecure"),
			"Unknown insecure",
			"The provider cannot be configured with an unknown value for insecure.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Extract plain Go values (null → zero value).
	endpoint := cfg.Endpoint.ValueString()
	token := cfg.Token.ValueString()
	var timeoutSecs int64
	if !cfg.RequestTimeout.IsNull() {
		timeoutSecs = cfg.RequestTimeout.ValueInt64()
	}
	var insecure bool
	if !cfg.Insecure.IsNull() {
		insecure = cfg.Insecure.ValueBool()
	}

	c, diags := resolveClient(endpoint, token, timeoutSecs, insecure)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.ResourceData = c
	resp.DataSourceData = c
}

// resolveClient applies environment-variable fallbacks, validates that
// endpoint and token are non-empty, and builds a *client.Client.
//
// This is intentionally extracted from Configure so that the env-fallback and
// validation logic can be unit-tested without constructing framework
// ConfigureRequest / ConfigureResponse objects.
//
// Parameters are plain Go values; callers treat null config attrs as "".
// A zero timeoutSecs means "use the client default (30 s)".
func resolveClient(endpoint, token string, timeoutSecs int64, insecure bool) (*client.Client, diag.Diagnostics) {
	var diags diag.Diagnostics

	// Env-variable fallbacks.
	if endpoint == "" {
		endpoint = os.Getenv("IAAS_API_ENDPOINT")
	}
	if token == "" {
		token = os.Getenv("IAAS_API_TOKEN")
	}

	// Validate required fields.
	if endpoint == "" {
		diags.AddAttributeError(
			path.Root("endpoint"),
			"Missing IaaS API Endpoint",
			"The provider requires an API endpoint. "+
				"Set the endpoint attribute in the provider configuration or set the IAAS_API_ENDPOINT environment variable.",
		)
	}
	if token == "" {
		diags.AddAttributeError(
			path.Root("token"),
			"Missing IaaS API Token",
			"The provider requires an API token. "+
				"Set the token attribute in the provider configuration or set the IAAS_API_TOKEN environment variable.",
		)
	}
	if diags.HasError() {
		return nil, diags
	}

	// Build the timeout duration; zero means client.New uses its 30 s default.
	var timeout time.Duration
	if timeoutSecs > 0 {
		timeout = time.Duration(timeoutSecs) * time.Second
	}

	c := client.New(endpoint, token, timeout, insecure)
	return c, diags
}

// Resources returns the list of resources provided.
func (p *IaasProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewSSHKeyResource,
		resources.NewVPCResource,
		resources.NewVPCSubnetResource,
		resources.NewInstanceResource,
		resources.NewProjectResource,
		resources.NewStaticIPResource,
		resources.NewIPSetResource,
		resources.NewSecurityGroupResource,
		resources.NewVolumeResource,
		resources.NewVolumeSnapshotResource,
		resources.NewNATGatewayResource,
		resources.NewLoadBalancerResource,
		resources.NewLBBackendResource,
		resources.NewLBTargetResource,
		resources.NewLBFrontendResource,
		resources.NewLBRoutingRuleResource,
		resources.NewLBCertificateResource,
		resources.NewVPNGatewayResource,
		resources.NewVPNPeerResource,
		resources.NewDNSZoneResource,
		resources.NewDNSRecordSetResource,
		resources.NewDNSRecordResource,
		resources.NewManagedDatabaseResource,
		resources.NewDBReplicaResource,
		resources.NewDBParameterGroupResource,
		resources.NewS3BucketResource,
		resources.NewS3AccessKeyResource,
		resources.NewInstanceBackupPolicyResource,
		resources.NewDBBackupPolicyResource,
		resources.NewNotificationChannelResource,
		resources.NewAlertRuleResource,
		resources.NewAutoscalingGroupResource,
		resources.NewAutoscalingPolicyResource,
		resources.NewKubernetesClusterResource,
		resources.NewKubernetesNodePoolResource,
		resources.NewKubernetesSslCertificateResource,
		resources.NewKubernetesSecurityGroupRuleResource,
		resources.NewUserScriptResource,
		resources.NewImageResource,
		resources.NewInstanceVpcAttachmentResource,
		resources.NewDockerDeploymentResource,
	}
}

// DataSources returns the list of data sources provided. These are the
// account whoami singleton, the instance-essential catalog lookups (location,
// plan, image, iso), the VPN peer config download, and the Kubernetes data
// sources (kubeconfig + autoscaler manifest downloads, the cluster-create
// catalog lookups for version / region / plan, and the vpc / subnet catalog
// lookups used to resolve vpc_id / subnet_id).
func (p *IaasProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasources.NewAccountDataSource,
		datasources.NewLocationDataSource,
		datasources.NewPlanDataSource,
		datasources.NewImageDataSource,
		datasources.NewISODataSource,
		datasources.NewVPNPeerConfigDataSource,
		datasources.NewKubernetesKubeconfigDataSource,
		datasources.NewKubernetesAutoscalerManifestDataSource,
		datasources.NewKubernetesVersionDataSource,
		datasources.NewKubernetesRegionDataSource,
		datasources.NewKubernetesPlanDataSource,
		datasources.NewKubernetesVPCDataSource,
		datasources.NewKubernetesSubnetDataSource,
	}
}
