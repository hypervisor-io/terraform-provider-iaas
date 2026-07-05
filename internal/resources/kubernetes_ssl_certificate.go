package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_kubernetes_ssl_certificate is a CHILD resource of
// iaas_kubernetes_cluster (Gap G6). It secures the cluster's CP load balancer:
// once an active cert exists, the cluster's Kubeconfig download endpoint
// rewrites `server:` to use the cert domain instead of the bare LB IP.
//
//   - the parent cluster UUID lives in the URL path (cluster_id, RequiresReplace);
//   - there is NO update route → every field is RequiresReplace (rotate by
//     replacing, same shape as iaas_lb_certificate);
//   - there is NO per-cert SHOW route → Read is LIST-and-match (cluster
//     ssl-certificates index) with a synthesised 404;
//   - import takes a COMPOSITE id "<cluster_id>/<cert_id>";
//   - writes carry idempotency.user (Create AND Delete), unlike
//     iaas_lb_certificate's plain LB endpoints.
//
// KEY DEVIATION vs iaas_lb_certificate: the cluster-scoped LIST
// (SslCertController::index) explicitly SELECTs only
// [id,name,type,domain,san_domains,expires_at,letsencrypt_status,
// letsencrypt_error,letsencrypt_domains,created_at] - certificate, private_key
// AND chain are NEVER returned, even right after create (private_key is
// additionally $hidden model-wide; certificate/chain are simply omitted from
// the index() query, unlike the plain load-balancer certificates[] embed which
// DOES return certificate/chain). So all three are write-only here: echoed
// from the plan on Create and preserved verbatim across every Read.
//
// SOURCE vs TYPE: the store body requires "source" ("letsencrypt"|"custom"),
// but the API never echoes "source" back - the persisted row instead reports
// "type" (DB enum "manual"|"letsencrypt"; source=custom maps to type=manual).
// Read derives `source` from the returned `type` (letsencrypt -> "letsencrypt",
// anything else -> "custom") so a composite import populates the Required
// `source` attribute without needing it in the API response.
var (
	_ resource.Resource                     = &kubernetesSslCertResource{}
	_ resource.ResourceWithConfigure        = &kubernetesSslCertResource{}
	_ resource.ResourceWithImportState      = &kubernetesSslCertResource{}
	_ resource.ResourceWithConfigValidators = &kubernetesSslCertResource{}
)

// NewKubernetesSslCertificateResource is the resource constructor registered
// with the provider.
func NewKubernetesSslCertificateResource() resource.Resource {
	return &kubernetesSslCertResource{}
}

// kubernetesSslCertResource manages an iaas_kubernetes_ssl_certificate - a TLS
// certificate on a Kubernetes cluster's CP load balancer.
type kubernetesSslCertResource struct {
	client *client.Client
}

// kubernetesSslCertModel maps the Terraform state/plan for
// iaas_kubernetes_ssl_certificate.
//
//   - parent (cluster_id): in the URL path → Required + RequiresReplace.
//   - create inputs: source, domain are Required + RequiresReplace; name,
//     san_domains, expires_at are Optional+Computed + RequiresReplaceIfConfigured
//     (the server may apply a default - e.g. name defaults to domain, LE forces
//     "LE: <domain>"); certificate/private_key/chain are write-only, Sensitive,
//     Optional (required_if source=custom, enforced by ConfigValidators) +
//     RequiresReplace.
//   - server-managed computed: type, letsencrypt_status, letsencrypt_error,
//     letsencrypt_domains - server-mutable, no UseStateForUnknown.
type kubernetesSslCertModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`

	Source      types.String `tfsdk:"source"`
	Domain      types.String `tfsdk:"domain"`
	Name        types.String `tfsdk:"name"`
	Certificate types.String `tfsdk:"certificate"`
	PrivateKey  types.String `tfsdk:"private_key"`
	Chain       types.String `tfsdk:"chain"`
	SanDomains  types.String `tfsdk:"san_domains"`
	ExpiresAt   types.String `tfsdk:"expires_at"`

	// Server-managed computed.
	Type               types.String `tfsdk:"type"`
	LetsencryptStatus  types.String `tfsdk:"letsencrypt_status"`
	LetsencryptError   types.String `tfsdk:"letsencrypt_error"`
	LetsencryptDomains types.List   `tfsdk:"letsencrypt_domains"`
}

// Metadata sets the resource type name → "<provider>_kubernetes_ssl_certificate".
func (r *kubernetesSslCertResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_ssl_certificate"
}

// Schema describes the iaas_kubernetes_ssl_certificate resource.
func (r *kubernetesSslCertResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a TLS certificate on a Kubernetes cluster's CP load balancer - either a " +
			"manually-uploaded PEM certificate (source = \"custom\") or a Let's Encrypt issuance " +
			"(source = \"letsencrypt\"). A certificate is a child of a cluster: its parent cluster_id " +
			"is part of the API path, so changing it forces a new resource. There is NO update route " +
			"- every field is immutable; changing any of them rotates (replaces) the certificate. " +
			"certificate, private_key and chain are WRITE-ONLY and SENSITIVE: the cluster-scoped list " +
			"endpoint never returns them (not even right after create), so they are taken from " +
			"configuration and never refreshed from the server. Once an active certificate exists, " +
			"the cluster's Kubeconfig download endpoint rewrites `server:` to use the certificate's " +
			"domain instead of the bare LB IP. Import with a composite id: \"<cluster_id>/<certificate_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the certificate, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent Kubernetes cluster this certificate belongs to. This " +
					"value is part of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source": schema.StringAttribute{
				Required: true,
				Description: "Certificate source: \"letsencrypt\" (ACME issuance for `domain` + any " +
					"`san_domains`) or \"custom\" (manual PEM upload - requires certificate + " +
					"private_key). Immutable; changing it forces a new resource. NOTE: the API does not " +
					"echo this field back; it is reconstructed on read from the persisted `type` " +
					"(\"letsencrypt\" stays \"letsencrypt\", anything else reads back as \"custom\").",
				Validators: []validator.String{
					stringvalidator.OneOf("letsencrypt", "custom"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"domain": schema.StringAttribute{
				Required: true,
				Description: "Primary certificate domain. The cluster's CP LB serves a single apiserver " +
					"endpoint, so this must be a single hostname (no lists). Immutable; changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name. Defaults to `domain` when omitted for source = \"custom\"; " +
					"REJECTED AT PLAN TIME for source = \"letsencrypt\" (enforced by ConfigValidators) - " +
					"the server always force-overrides it to \"LE: <domain>\", so setting it would only " +
					"ever surface as an inconsistent-apply error. Immutable; changing it forces a new " +
					"resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"certificate": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "PEM-encoded leaf certificate. Required when source = \"custom\" (enforced " +
					"at plan time); REJECTED AT PLAN TIME for source = \"letsencrypt\" (enforced by " +
					"ConfigValidators - the server ignores it in favour of the ACME-issued certificate). " +
					"WRITE-ONLY: the cluster ssl-certificates list never returns it, so it is taken from " +
					"configuration and never refreshed. Immutable; changing it forces a new resource " +
					"(rotation).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"private_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "PEM-encoded private key. Required when source = \"custom\" (enforced at " +
					"plan time); REJECTED AT PLAN TIME for source = \"letsencrypt\" (enforced by " +
					"ConfigValidators - the server ignores it in favour of the ACME-issued key). " +
					"WRITE-ONLY and SENSITIVE: never returned by the API (private_key is $hidden " +
					"model-wide), so it is taken from configuration and never refreshed. Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"chain": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Optional PEM-encoded intermediate certificate chain (source = \"custom\" " +
					"only - REJECTED AT PLAN TIME for source = \"letsencrypt\", enforced by " +
					"ConfigValidators). WRITE-ONLY: the cluster ssl-certificates list never returns it, " +
					"so it is taken from configuration and never refreshed. Immutable; changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"san_domains": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Comma-separated SAN domains (optional, either source - the server stores " +
					"and uses san_domains for a \"letsencrypt\" issuance too, so this is NOT rejected for " +
					"that source). Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expires_at": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Certificate expiry (nullable date; source = \"custom\" only - accepted by " +
					"the store validation though not documented on the endpoint - REJECTED AT PLAN TIME " +
					"for source = \"letsencrypt\", enforced by ConfigValidators, since ACME issuance " +
					"determines the real expiry). Null for a fresh \"letsencrypt\" cert until ACME " +
					"issuance completes. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ── Server-managed computed ───────────────────────────────────────
			// type / letsencrypt_status / letsencrypt_error / letsencrypt_domains
			// are SERVER-MUTABLE (an LE cert's status/error/domains evolve as ACME
			// issuance progresses in the background). Per the golden guardrail, do
			// NOT attach UseStateForUnknown to server-mutable computed fields.
			"type": schema.StringAttribute{
				Computed: true,
				Description: "Persisted certificate type: \"manual\" (source = \"custom\") or " +
					"\"letsencrypt\". Server-mutable in principle (it is the source of truth `source` " +
					"is derived from on read).",
			},
			"letsencrypt_status": schema.StringAttribute{
				Computed: true,
				Description: "Let's Encrypt issuance status (e.g. \"pending_dns\", \"active\", " +
					"\"error\"). Empty for source = \"custom\". Server-mutable - evolves in the " +
					"background as ACME issuance progresses; this resource does not wait/poll for it.",
			},
			"letsencrypt_error": schema.StringAttribute{
				Computed:    true,
				Description: "Let's Encrypt issuance error message, if any. Server-mutable.",
			},
			"letsencrypt_domains": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Full set of domains covered by a Let's Encrypt cert (domain + " +
					"san_domains, as accepted by ACME). Null for source = \"custom\". Server-mutable.",
			},
		},
	}
}

// ConfigValidators enforces the store endpoint's required_if:source,custom rule
// (StoreClusterSslCertRequest) at plan time: certificate + private_key must be
// set when source = "custom". Both are ignored (and may be omitted) for
// source = "letsencrypt". The sibling validator below enforces the reverse
// direction: name/certificate/private_key/chain/expires_at are REJECTED for
// source = "letsencrypt", since the server force-overrides or ignores them.
func (r *kubernetesSslCertResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		&kubernetesSslCertCustomFieldsValidator{},
		&kubernetesSslCertLetsencryptFieldsValidator{},
	}
}

// kubernetesSslCertCustomFieldsValidator implements resource.ConfigValidator.
type kubernetesSslCertCustomFieldsValidator struct{}

func (v *kubernetesSslCertCustomFieldsValidator) Description(_ context.Context) string {
	return "Requires certificate and private_key when source = \"custom\" (mirrors the Master API's required_if validation)."
}

func (v *kubernetesSslCertCustomFieldsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *kubernetesSslCertCustomFieldsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg kubernetesSslCertModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// source is Required; skip only if genuinely unknown (e.g. derived from
	// another unknown value elsewhere in the config graph).
	if cfg.Source.IsUnknown() || cfg.Source.IsNull() {
		return
	}
	if cfg.Source.ValueString() != "custom" {
		return
	}

	if cfg.Certificate.IsUnknown() {
		return
	}
	if cfg.Certificate.IsNull() || cfg.Certificate.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("certificate"),
			"Missing Required Field",
			"certificate is required when source = \"custom\".",
		)
	}
	if cfg.PrivateKey.IsUnknown() {
		return
	}
	if cfg.PrivateKey.IsNull() || cfg.PrivateKey.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("private_key"),
			"Missing Required Field",
			"private_key is required when source = \"custom\".",
		)
	}
}

// kubernetesSslCertLetsencryptFieldsValidator implements resource.ConfigValidator.
// It rejects name/certificate/private_key/chain/expires_at when
// source = "letsencrypt": the server FORCE-OVERRIDES name to "LE: <domain>"
// and IGNORES certificate/private_key/chain/expires_at entirely for an ACME
// issuance (StoreClusterSslCertRequest only conditionally requires them for
// source = "custom" - see kubernetesSslCertCustomFieldsValidator above).
// Before this validator existed, only the "custom" branch was checked, so
// setting any of these fields alongside source = "letsencrypt" reached Create,
// which echoes them into state from the plan (kubernetesSslCertStateFromAPI) -
// the very next Read/plan then observes the server's ACTUAL persisted values
// (a force-overridden name, or certificate/private_key/chain/expires_at simply
// absent from the list response) and crashes with "Provider produced
// inconsistent result after apply". Rejecting them at plan time (mirrors
// iaas_docker_deployment's forbid-list for source = "app") is cheaper and
// clearer than papering over that mismatch. san_domains is deliberately NOT
// forbidden: the server stores and uses san_domains for a "letsencrypt"
// issuance too (it feeds the ACME SAN list), so it is a legitimate input for
// either source.
type kubernetesSslCertLetsencryptFieldsValidator struct{}

func (v *kubernetesSslCertLetsencryptFieldsValidator) Description(_ context.Context) string {
	return "Rejects name/certificate/private_key/chain/expires_at when source = \"letsencrypt\" " +
		"(the server force-overrides or ignores them for an ACME issuance)."
}

func (v *kubernetesSslCertLetsencryptFieldsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *kubernetesSslCertLetsencryptFieldsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg kubernetesSslCertModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// source is Required; skip only if genuinely unknown (e.g. derived from
	// another unknown value elsewhere in the config graph).
	if cfg.Source.IsUnknown() || cfg.Source.IsNull() {
		return
	}
	if cfg.Source.ValueString() != "letsencrypt" {
		return
	}

	// Don't evaluate presence checks against an unknown value (e.g. derived
	// from another resource) - defer to a later validation pass.
	if cfg.Name.IsUnknown() || cfg.Certificate.IsUnknown() || cfg.PrivateKey.IsUnknown() ||
		cfg.Chain.IsUnknown() || cfg.ExpiresAt.IsUnknown() {
		return
	}

	if !cfg.Name.IsNull() && cfg.Name.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("name"),
			"Invalid Field for source = \"letsencrypt\"",
			`name is always set to "LE: <domain>" by the server for source = "letsencrypt" and cannot `+
				`be overridden; omit it, or use source = "custom" to set a custom name.`,
		)
	}
	if !cfg.Certificate.IsNull() && cfg.Certificate.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("certificate"),
			"Invalid Field for source = \"letsencrypt\"",
			`certificate is ignored by the server for source = "letsencrypt" (the ACME-issued `+
				`certificate replaces it); omit it, or use source = "custom" to upload one.`,
		)
	}
	if !cfg.PrivateKey.IsNull() && cfg.PrivateKey.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("private_key"),
			"Invalid Field for source = \"letsencrypt\"",
			`private_key is ignored by the server for source = "letsencrypt" (the ACME-issued key `+
				`replaces it); omit it, or use source = "custom" to upload one.`,
		)
	}
	if !cfg.Chain.IsNull() && cfg.Chain.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("chain"),
			"Invalid Field for source = \"letsencrypt\"",
			`chain is ignored by the server for source = "letsencrypt"; omit it, or use source = "custom" to upload one.`,
		)
	}
	if !cfg.ExpiresAt.IsNull() && cfg.ExpiresAt.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("expires_at"),
			"Invalid Field for source = \"letsencrypt\"",
			`expires_at is ignored by the server for source = "letsencrypt" (ACME issuance determines `+
				`the real expiry); omit it, or use source = "custom" to set it.`,
		)
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *kubernetesSslCertResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data Type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", req.ProviderData),
		)
		return
	}
	r.client = c
}

// Create uploads/requests the certificate on its parent cluster's CP load
// balancer (synchronous at the row level - Let's Encrypt issuance itself may
// still be pending_dns in the background; this resource does not poll it). A
// STABLE idempotency key derived from the immutable create inputs makes a
// lost-response retry safe. The id is persisted before the read-back so a
// failed scan still tracks the resource for cleanup. Read-back is by
// LIST-and-match; certificate/private_key/chain are never in that response, so
// the state mapper always takes them from the plan, not the API object.
func (r *kubernetesSslCertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesSslCertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := sslCertCreateBody(plan)
	clusterID := plan.ClusterID.ValueString()

	created, err := r.client.CreateKubernetesSslCert(ctx, clusterID, body, idempotencyKeyForSslCert(clusterID, body))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating Kubernetes cluster SSL certificate", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster SSL certificate", "the create response did not include a certificate id")
		return
	}

	// Persist the id immediately so a failed read-back still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read back via LIST-and-match so state reflects the server-applied
	// defaults (name, type) that the raw create response may not carry (e.g.
	// a DB-default column not explicitly passed to create() is absent from
	// the in-memory model until the row is re-queried). Fall back to the raw
	// create response if the scan can't find it yet (defensive).
	obj, err := r.client.GetKubernetesSslCert(ctx, clusterID, id)
	if err != nil {
		obj = created
	}

	state, diags := kubernetesSslCertStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state via LIST-and-match (no per-cert SHOW). A not-found
// (cert absent from the list, or the parent cluster errors) removes the
// resource from state so Terraform plans a recreate.
func (r *kubernetesSslCertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesSslCertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetKubernetesSslCert(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes cluster SSL certificate", err))
		return
	}

	next, diags := kubernetesSslCertStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Update is unreachable: every field is RequiresReplace (there is no
// certificate update endpoint on the cluster-scoped API either), so the
// framework recreates rather than updating. Implemented as a pass-through to
// satisfy the resource.Resource interface.
func (r *kubernetesSslCertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan kubernetesSslCertModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the certificate from the cluster's CP load balancer. The
// route carries idempotency.user; "" lets the client generate a fresh key.
func (r *kubernetesSslCertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesSslCertModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteKubernetesSslCert(ctx, state.ClusterID.ValueString(), state.ID.ValueString(), ""); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting Kubernetes cluster SSL certificate", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource:
//
//	terraform import iaas_kubernetes_ssl_certificate.x <cluster_id>/<cert_id>
//
// certificate/private_key/chain cannot be read back (write-only) and land null;
// set them in configuration after importing (same limitation documented on
// iaas_lb_certificate).
func (r *kubernetesSslCertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, certID, ok := strings.Cut(req.ID, "/")
	if !ok || clusterID == "" || certID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"cluster_id/certificate_id\", got: %q. "+
				"SSL certificates are child resources, so both the parent cluster id and the "+
				"certificate id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), certID)...)
}

// sslCertCreateBody builds the create request body matching
// StoreClusterSslCertRequest's own field set exactly (source, domain, +
// conditional name/certificate/private_key/chain/san_domains/expires_at).
func sslCertCreateBody(plan kubernetesSslCertModel) map[string]any {
	body := map[string]any{
		"source": plan.Source.ValueString(),
		"domain": plan.Domain.ValueString(),
	}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && plan.Name.ValueString() != "" {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.Certificate.IsNull() && !plan.Certificate.IsUnknown() {
		body["certificate"] = plan.Certificate.ValueString()
	}
	if !plan.PrivateKey.IsNull() && !plan.PrivateKey.IsUnknown() {
		body["private_key"] = plan.PrivateKey.ValueString()
	}
	if !plan.Chain.IsNull() && !plan.Chain.IsUnknown() && plan.Chain.ValueString() != "" {
		body["chain"] = plan.Chain.ValueString()
	}
	if !plan.SanDomains.IsNull() && !plan.SanDomains.IsUnknown() && plan.SanDomains.ValueString() != "" {
		body["san_domains"] = plan.SanDomains.ValueString()
	}
	if !plan.ExpiresAt.IsNull() && !plan.ExpiresAt.IsUnknown() && plan.ExpiresAt.ValueString() != "" {
		body["expires_at"] = plan.ExpiresAt.ValueString()
	}
	return body
}

// kubernetesSslCertStateFromAPI builds the model from an API certificate
// object (the create response or the LIST scan). cluster_id is never in the
// body (it is in the path) so it always falls back to the prior plan/state
// value. certificate, private_key and chain are NEVER in the API response
// (list-and-create alike) - they are taken from prior/plan UNCONDITIONALLY.
// source is derived from the persisted `type`, not read from the body (the API
// never echoes `source`).
func kubernetesSslCertStateFromAPI(ctx context.Context, obj map[string]any, prior kubernetesSslCertModel) (kubernetesSslCertModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	letsencryptDomains, d := letsencryptDomainsFromAPI(ctx, obj)
	diags.Append(d...)

	m := kubernetesSslCertModel{
		ID:        stringFromAPI(obj, "id", prior.ID),
		ClusterID: prior.ClusterID, // never in the response body; from the path

		Source: sourceFromType(obj, prior.Source),
		Domain: stringFromAPI(obj, "domain", prior.Domain),
		Name:   stringFromAPI(obj, "name", settleOptionalString(prior.Name)),

		// WRITE-ONLY - never in the LIST/create response; preserve verbatim.
		Certificate: prior.Certificate,
		PrivateKey:  prior.PrivateKey,
		Chain:       prior.Chain,

		SanDomains: sanDomainsFromAPI(obj, settleOptionalString(prior.SanDomains)),
		ExpiresAt:  optionalStringFromAPI(obj, "expires_at", settleOptionalString(prior.ExpiresAt)),

		Type:               computedStringFromAPI(obj, "type", prior.Type),
		LetsencryptStatus:  computedStringFromAPI(obj, "letsencrypt_status", prior.LetsencryptStatus),
		LetsencryptError:   computedStringFromAPI(obj, "letsencrypt_error", prior.LetsencryptError),
		LetsencryptDomains: letsencryptDomains,
	}
	return m, diags
}

// settleOptionalString guards the "prior" fallback passed to stringFromAPI /
// optionalStringFromAPI / sanDomainsFromAPI for the Optional+Computed
// attributes (name, san_domains, expires_at). On a first Create with the
// attribute omitted from config, the PLAN value is Unknown (there is no prior
// STATE to inherit from - UseStateForUnknown only helps on Update). In real
// production traffic Eloquent always serialises every selected column
// (including nulls), so the "absent key" fallback branch should never fire -
// but this settles it to a KNOWN null defensively so a response that omits a
// key can never leak an Unknown value into state (which Terraform rejects as
// an inconsistent apply result).
func settleOptionalString(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}
	return v
}

// sourceFromType derives the write-input `source` from the persisted `type`
// column ("manual"|"letsencrypt") since the API never echoes `source` back.
// An absent/null type falls back to the prior value (defensive; type is a
// non-nullable DB column with a default, so this should not occur in practice).
func sourceFromType(obj map[string]any, fallback types.String) types.String {
	raw, ok := obj["type"]
	if !ok || raw == nil {
		return fallback
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return fallback
	}
	if s == "letsencrypt" {
		return types.StringValue("letsencrypt")
	}
	return types.StringValue("custom")
}

// sanDomainsFromAPI reads the "san_domains" field, tolerating BOTH shapes the
// Master API can return: a plain comma-separated string (source = "custom" -
// the manual-upload path stores the raw request string, which round-trips
// through the model's `array` cast as a JSON string rather than a real array)
// or a genuine JSON array of domains (source = "letsencrypt" - built from a
// real PHP array server-side). A present null or empty value settles to a
// KNOWN null (not the prior/fallback) so an Optional+Computed attribute never
// leaks an unknown value into state; only a truly ABSENT key falls back.
func sanDomainsFromAPI(obj map[string]any, fallback types.String) types.String {
	raw, ok := obj["san_domains"]
	if !ok {
		return fallback
	}
	if raw == nil {
		return types.StringNull()
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return types.StringNull()
		}
		return types.StringValue(v)
	case []any:
		if len(v) == 0 {
			return types.StringNull()
		}
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return types.StringNull()
		}
		return types.StringValue(strings.Join(parts, ","))
	default:
		return types.StringNull()
	}
}

// letsencryptDomainsFromAPI converts the API "letsencrypt_domains" field (a
// real JSON array for Let's Encrypt certs, absent/null for manual/custom) to a
// types.List(string). Always resolves to a KNOWN value (null list or a
// populated one) - this is a Computed-only field, never a plan fallback.
func letsencryptDomainsFromAPI(ctx context.Context, obj map[string]any) (types.List, diag.Diagnostics) {
	raw, ok := obj["letsencrypt_domains"]
	if !ok || raw == nil {
		return types.ListNull(types.StringType), nil
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return types.ListNull(types.StringType), nil
	}
	items := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			items = append(items, s)
		}
	}
	if len(items) == 0 {
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, items)
}

// idempotencyKeyForSslCert derives a STABLE idempotency key from the parent
// cluster id + the create body so a re-applied identical config reuses the
// same key, letting the Master's idempotency.user middleware replay its
// cached 2xx response (for 24h) instead of creating a SECOND certificate when
// a create's HTTP response was lost but the server-side create succeeded.
func idempotencyKeyForSslCert(clusterID string, body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	fmt.Fprintf(&sb, "cluster=%s;", clusterID)
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%v;", k, body[k])
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return "tf-k8s-sslcert-" + hex.EncodeToString(sum[:])
}
