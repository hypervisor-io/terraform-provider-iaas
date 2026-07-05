package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_kubernetes_security_group_rule is a STANDALONE
// CHILD resource of a Kubernetes cluster's auto-provisioned "lb"/"cp"/"worker"
// security group (Gap G7). Unlike iaas_security_group's nested `rules`
// SetNestedAttribute (rules owned inline by the parent SG), each rule here is
// its own resource - keyed by (cluster_id, scope, id) - because the cluster's
// SG is not itself a Terraform-managed resource (it is auto-provisioned at
// cluster-create time, not created by this provider).
//
//   - cluster_id + scope are both part of the API path → Required +
//     RequiresReplace.
//   - scope is constrained to exactly {"lb","cp","worker"} (mirrors the route's
//     `where(['scope' => 'lb|cp|worker'])`, itself backed by
//     SecurityGroupController::CLUSTER_SG_SCOPES).
//   - there is no update route (add-only) → every rule field is
//     RequiresReplace, same shape as iaas_kubernetes_ssl_certificate.
//   - there is no per-rule SHOW route → Read is LIST-and-match (rules for
//     cluster+scope) with a synthesised 404.
//   - import takes a 3-PART composite id "<cluster_id>/<scope>/<rule_id>"
//     (same shape as iaas_lb_target's load_balancer_id/backend_id/target_id).
//   - writes carry idempotency.user (Create AND Delete), same as
//     iaas_kubernetes_ssl_certificate.
var (
	_ resource.Resource                     = &kubernetesSecurityGroupRuleResource{}
	_ resource.ResourceWithConfigure        = &kubernetesSecurityGroupRuleResource{}
	_ resource.ResourceWithImportState      = &kubernetesSecurityGroupRuleResource{}
	_ resource.ResourceWithConfigValidators = &kubernetesSecurityGroupRuleResource{}
)

// NewKubernetesSecurityGroupRuleResource is the resource constructor registered
// with the provider.
func NewKubernetesSecurityGroupRuleResource() resource.Resource {
	return &kubernetesSecurityGroupRuleResource{}
}

// kubernetesSecurityGroupRuleResource manages a single firewall rule on one of
// a Kubernetes cluster's auto-provisioned security groups (lb/cp/worker).
type kubernetesSecurityGroupRuleResource struct {
	client *client.Client
}

// kubernetesSecurityGroupRuleModel maps the Terraform state/plan for
// iaas_kubernetes_security_group_rule. Every writable field is RequiresReplace
// - the API has no rule-update endpoint, so any change is a delete+add.
// security_group_id and internal are server-only Computed fields (the resolved
// scope SG's id, and whether the rule is one of the cluster's own
// auto-provisioned rules - deleting an `internal` rule may break connectivity).
type kubernetesSecurityGroupRuleModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Scope     types.String `tfsdk:"scope"`

	Direction     types.String `tfsdk:"direction"`
	Protocol      types.String `tfsdk:"protocol"`
	PortRangeMin  types.Int64  `tfsdk:"port_range_min"`
	PortRangeMax  types.Int64  `tfsdk:"port_range_max"`
	IPVersion     types.String `tfsdk:"ip_version"`
	Cidr          types.String `tfsdk:"cidr"`
	RemoteGroupID types.String `tfsdk:"remote_group_id"`
	IPSetID       types.String `tfsdk:"ip_set_id"`
	Description   types.String `tfsdk:"description"`

	// Server-managed computed.
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	Internal        types.Bool   `tfsdk:"internal"`
}

// Metadata sets the resource type name → "<provider>_kubernetes_security_group_rule".
func (r *kubernetesSecurityGroupRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_security_group_rule"
}

// Schema describes the iaas_kubernetes_security_group_rule resource.
func (r *kubernetesSecurityGroupRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single firewall rule on one of a Kubernetes cluster's " +
			"auto-provisioned security groups: `lb` (internet-facing apiserver ingress, attached " +
			"to the CP load balancer instance), `cp` (control-plane node ingress) or `worker` " +
			"(worker node ingress). The security groups themselves are provisioned automatically " +
			"when the cluster is created (they are not a Terraform-managed resource); this " +
			"resource only adds/removes individual rules on them. There is NO update route - " +
			"changing any field replaces the rule (delete old + add new). Import with a 3-part " +
			"composite id: \"<cluster_id>/<scope>/<rule_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the rule, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent Kubernetes cluster this rule belongs to. Part of " +
					"the API path; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scope": schema.StringAttribute{
				Required: true,
				Description: "Which of the cluster's auto-provisioned security groups to target: " +
					"`lb`, `cp`, or `worker`. Part of the API path; changing it forces a new " +
					"resource.",
				Validators: []validator.String{
					stringvalidator.OneOf("lb", "cp", "worker"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"direction": schema.StringAttribute{
				Required:    true,
				Description: "Traffic direction: \"ingress\" (inbound) or \"egress\" (outbound).",
				Validators: []validator.String{
					stringvalidator.OneOf("ingress", "egress"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol": schema.StringAttribute{
				Required: true,
				Description: "Protocol the rule matches: \"tcp\", \"udp\", \"icmp\", \"icmpv6\", " +
					"\"all\", or \"any\". Ports (port_range_min/port_range_max) are required for " +
					"\"tcp\"/\"udp\". NOTE: the Master API's `security_group_rules.protocol` DB " +
					"column has no \"any\" enum member, so submitting protocol = \"any\" is accepted " +
					"by the request validator but currently fails at insert time with a 422 - this " +
					"is a Master-side inconsistency, not a client-side restriction.",
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "udp", "icmp", "icmpv6", "all", "any"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"port_range_min": schema.Int64Attribute{
				Optional: true,
				Description: "Inclusive lower port (1-65535). Required for tcp/udp; ignored for " +
					"icmp/icmpv6/all/any.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"port_range_max": schema.Int64Attribute{
				Optional: true,
				Description: "Inclusive upper port (1-65535). Required for tcp/udp; ignored for " +
					"icmp/icmpv6/all/any.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"ip_version": schema.StringAttribute{
				Required:    true,
				Description: "Address family: \"ipv4\" or \"ipv6\".",
				Validators: []validator.String{
					stringvalidator.OneOf("ipv4", "ipv6"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cidr": schema.StringAttribute{
				Optional: true,
				Description: "CIDR source (ingress) or destination (egress), e.g. \"10.0.0.0/8\". " +
					"Mutually exclusive with remote_group_id and ip_set_id - exactly one of the " +
					"three must be set.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"remote_group_id": schema.StringAttribute{
				Optional: true,
				Description: "UUID of a security group whose members are the rule's source/" +
					"destination. Mutually exclusive with cidr and ip_set_id.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ip_set_id": schema.StringAttribute{
				Optional: true,
				Description: "UUID of an IP set whose entries are the rule's source/destination. " +
					"Mutually exclusive with cidr and remote_group_id.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Optional free-form note (max 255 characters).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// ── Server-managed computed ───────────────────────────────────────
			"security_group_id": schema.StringAttribute{
				Computed: true,
				Description: "UUID of the resolved scope security group this rule belongs to " +
					"(the cluster's lb/cp/worker SG, per `scope`).",
			},
			"internal": schema.BoolAttribute{
				Computed: true,
				Description: "Whether this is one of the cluster's own auto-provisioned rules " +
					"(e.g. the SSH/apiserver access rules created alongside the cluster) rather " +
					"than a user-added one. Deleting an internal rule may break cluster " +
					"connectivity; this resource does not prevent it.",
			},
		},
	}
}

// ConfigValidators enforces the store endpoint's mutual-exclusivity rule
// (SecurityGroupService::addRule): exactly one of cidr, remote_group_id, or
// ip_set_id must be set - mirrors kubernetes_ssl_certificate.go's
// source-conditional validators.
func (r *kubernetesSecurityGroupRuleResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		&kubernetesSecurityGroupRuleTargetValidator{},
	}
}

// kubernetesSecurityGroupRuleTargetValidator implements resource.ConfigValidator.
type kubernetesSecurityGroupRuleTargetValidator struct{}

func (v *kubernetesSecurityGroupRuleTargetValidator) Description(_ context.Context) string {
	return "Requires exactly one of cidr, remote_group_id, or ip_set_id (mirrors the Master API's " +
		"SecurityGroupService::addRule mutual-exclusivity rule)."
}

func (v *kubernetesSecurityGroupRuleTargetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *kubernetesSecurityGroupRuleTargetValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg kubernetesSecurityGroupRuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Don't evaluate presence checks against an unknown value (e.g. derived
	// from another resource) - defer to a later validation pass.
	if cfg.Cidr.IsUnknown() || cfg.RemoteGroupID.IsUnknown() || cfg.IPSetID.IsUnknown() {
		return
	}

	hasCidr := !cfg.Cidr.IsNull() && cfg.Cidr.ValueString() != ""
	hasRemoteGroup := !cfg.RemoteGroupID.IsNull() && cfg.RemoteGroupID.ValueString() != ""
	hasIPSet := !cfg.IPSetID.IsNull() && cfg.IPSetID.ValueString() != ""

	count := 0
	for _, set := range []bool{hasCidr, hasRemoteGroup, hasIPSet} {
		if set {
			count++
		}
	}

	switch {
	case count == 0:
		resp.Diagnostics.AddError(
			"Missing Required Field",
			"exactly one of cidr, remote_group_id, or ip_set_id is required (mirrors the Master "+
				"API's SecurityGroupService::addRule rule).",
		)
	case count > 1:
		resp.Diagnostics.AddError(
			"Invalid Field Combination",
			"cidr, remote_group_id, and ip_set_id are mutually exclusive - set exactly one (mirrors "+
				"the Master API's SecurityGroupService::addRule rule).",
		)
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *kubernetesSecurityGroupRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create adds the rule to the cluster's scope security group, then reads it
// back by scan so state reflects the server-assigned id and any
// server-populated fields (security_group_id, internal).
func (r *kubernetesSecurityGroupRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesSecurityGroupRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := plan.ClusterID.ValueString()
	scope := plan.Scope.ValueString()
	body := sgRuleCreateBody(plan)

	created, err := r.client.CreateKubernetesClusterSgRule(ctx, clusterID, scope, body, idempotencyKeyForSgRule(clusterID, scope, body))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating Kubernetes cluster security group rule", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster security group rule", "the create response did not include a rule id")
		return
	}

	// Persist the id immediately so a failed read-back still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetKubernetesClusterSgRule(ctx, clusterID, scope, id)
	if err != nil {
		obj = created
	}

	state := kubernetesSecurityGroupRuleStateFromAPI(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state via LIST-and-match (no per-rule SHOW). A not-found
// (rule absent from the list, or the parent cluster/scope errors) removes the
// resource from state so Terraform plans a recreate.
func (r *kubernetesSecurityGroupRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesSecurityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetKubernetesClusterSgRule(ctx, state.ClusterID.ValueString(), state.Scope.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes cluster security group rule", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, kubernetesSecurityGroupRuleStateFromAPI(obj, state))...)
}

// Update is unreachable: every field is RequiresReplace (there is no rule
// update endpoint), so the framework recreates rather than updating.
// Implemented as a pass-through to satisfy the resource.Resource interface.
func (r *kubernetesSecurityGroupRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan kubernetesSecurityGroupRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the rule from the cluster's scope security group. The route
// carries idempotency.user; "" lets the client generate a fresh key.
func (r *kubernetesSecurityGroupRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesSecurityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteKubernetesClusterSgRule(ctx, state.ClusterID.ValueString(), state.Scope.ValueString(), state.ID.ValueString(), ""); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting Kubernetes cluster security group rule", err))
		return
	}
}

// ImportState implements 3-PART COMPOSITE import:
//
//	terraform import iaas_kubernetes_security_group_rule.x <cluster_id>/<scope>/<rule_id>
func (r *kubernetesSecurityGroupRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"cluster_id/scope/rule_id\", got: %q. "+
				"Security group rules are child resources of a cluster's scope security group, so "+
				"all three ids are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("scope"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

// sgRuleCreateBody builds the create request body matching
// StoreClusterSgRuleRequest's own field set exactly (direction, protocol,
// ip_version required; port_range_min/max, cidr, remote_group_id, ip_set_id,
// description conditional/optional).
func sgRuleCreateBody(plan kubernetesSecurityGroupRuleModel) map[string]any {
	body := map[string]any{
		"direction":  plan.Direction.ValueString(),
		"protocol":   plan.Protocol.ValueString(),
		"ip_version": plan.IPVersion.ValueString(),
	}
	if !plan.PortRangeMin.IsNull() && !plan.PortRangeMin.IsUnknown() {
		body["port_range_min"] = plan.PortRangeMin.ValueInt64()
	}
	if !plan.PortRangeMax.IsNull() && !plan.PortRangeMax.IsUnknown() {
		body["port_range_max"] = plan.PortRangeMax.ValueInt64()
	}
	if !plan.Cidr.IsNull() && !plan.Cidr.IsUnknown() && plan.Cidr.ValueString() != "" {
		body["cidr"] = plan.Cidr.ValueString()
	}
	if !plan.RemoteGroupID.IsNull() && !plan.RemoteGroupID.IsUnknown() && plan.RemoteGroupID.ValueString() != "" {
		body["remote_group_id"] = plan.RemoteGroupID.ValueString()
	}
	if !plan.IPSetID.IsNull() && !plan.IPSetID.IsUnknown() && plan.IPSetID.ValueString() != "" {
		body["ip_set_id"] = plan.IPSetID.ValueString()
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	return body
}

// kubernetesSecurityGroupRuleStateFromAPI builds the model from an API rule
// object (the create response or the LIST scan). cluster_id/scope are never in
// the body (they are in the path) so they always fall back to the prior
// plan/state value.
func kubernetesSecurityGroupRuleStateFromAPI(obj map[string]any, prior kubernetesSecurityGroupRuleModel) kubernetesSecurityGroupRuleModel {
	return kubernetesSecurityGroupRuleModel{
		ID:        stringFromAPI(obj, "id", prior.ID),
		ClusterID: prior.ClusterID, // never in the response body; from the path
		Scope:     prior.Scope,     // never in the response body; from the path

		Direction:     stringFromAPI(obj, "direction", prior.Direction),
		Protocol:      stringFromAPI(obj, "protocol", prior.Protocol),
		PortRangeMin:  optionalInt64FromAPI(obj, "port_range_min"),
		PortRangeMax:  optionalInt64FromAPI(obj, "port_range_max"),
		IPVersion:     stringFromAPI(obj, "ip_version", prior.IPVersion),
		Cidr:          optionalStringFromAPI(obj, "cidr", prior.Cidr),
		RemoteGroupID: optionalStringFromAPI(obj, "remote_group_id", prior.RemoteGroupID),
		IPSetID:       optionalStringFromAPI(obj, "ip_set_id", prior.IPSetID),
		Description:   optionalStringFromAPI(obj, "description", prior.Description),

		SecurityGroupID: stringFromAPI(obj, "security_group_id", prior.SecurityGroupID),
		Internal:        boolFromIntAPI(obj, "internal", prior.Internal),
	}
}

// idempotencyKeyForSgRule derives a STABLE idempotency key from the parent
// cluster id + scope + the create body so a re-applied identical config reuses
// the same key, letting the Master's idempotency.user middleware replay its
// cached 2xx response (for 24h) instead of adding a SECOND rule when a
// create's HTTP response was lost but the server-side create succeeded.
func idempotencyKeyForSgRule(clusterID, scope string, body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	fmt.Fprintf(&sb, "cluster=%s;scope=%s;", clusterID, scope)
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%v;", k, body[k])
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return "tf-k8s-sgrule-" + hex.EncodeToString(sum[:])
}
