package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions — iaas_kubernetes_cluster is an ASYNC, MULTI-STAGE
// resource. It copies the established async recipe:
//
//   - ASYNC state-poll (from load_balancer/managed_database): CREATE records the
//     cluster row (state="created"), auto-creates the default node pool, and
//     dispatches the CreateCluster job; this resource then polls the cluster's
//     OWN "state" field via SHOW until "running" (the state machine drives
//     created → starting → running | error). There IS a tracking task_id in the
//     create response, but the cluster state is the canonical convergence signal
//     (it is what the UI polls), so we poll SHOW state — NOT the task. The id is
//     persisted to state BEFORE the wait so a failed wait still leaves a
//     destroyable resource; a timeouts block is exposed with a LARGE create
//     default (K8s provisioning is slow).
//   - IDEMPOTENCY: the create/update/delete routes carry the idempotency.user
//     middleware (reads the "Idempotency-Key" REQUEST header, replays the cached
//     2xx for 24h). To make a lost-response create retry safe, the resource
//     derives a STABLE idempotency key from the immutable create inputs
//     (idempotencyKeyForPlan) so a re-applied identical config reuses the same
//     key and the server deduplicates instead of spinning up a second cluster.
//   - MOSTLY-IMMUTABLE: name/description/project_id are mutable via PATCH
//     (controller's UpdateClusterRequest); every other topology input is
//     RequiresReplace EXCEPT kubernetes_version_id (T7/id-G8), which is now
//     updatable in place — see the version-upgrade orchestration below.
//   - VERSION UPGRADE (T7/id-G8): the Master tracks TWO version columns on the
//     cluster row, both equal at create — kubernetes_version_id (the WORKER
//     baseline) and cp_kubernetes_version_id (the control plane's current
//     version, exposed here read-only as cp_kubernetes_version_id/
//     cp_kubernetes_version). When the plan's kubernetes_version_id differs
//     from state, Update calls the dedicated upgrade endpoints in STAGED order
//     — control plane (POST .../upgrade/cp) only if it isn't already at the
//     target, then workers (POST .../upgrade/workers), matching the Master's
//     own upstream-safety invariant that kubelet must never run ahead of the
//     apiserver — then, if upgrade_ccm is true, a synchronous CCM redeploy
//     (POST .../upgrade/ccm) so the cloud-controller-manager image tracks the
//     new worker baseline. Each rolling-upgrade stage is async (a
//     KubernetesClusterTask, NOT a cluster.state change — state stays
//     "running" throughout); convergence is polled via
//     waitForClusterUpgradeTask, which scans the cluster's own embedded
//     "tasks" array (SHOW eager-loads the last 20) for the returned task_id's
//     status, reusing the SAME waiter.WaitFor primitive as create/delete. See
//     Update's doc comment for why POST .../upgrade/retry is deliberately NOT
//     wired into the upgrade failure path.
//
// This is the CORE cluster (+ its in-place version-upgrade lifecycle). Node
// pools (id32), default-pool workers/scale/labels/autoscaling (id33),
// kubeconfig + autoscaler-manifest + catalog search data sources (id34) are
// SEPARATE tasks and are NOT modelled here. The cluster CA cert / kubeconfig
// (Sensitive) are exposed by the id34 kubeconfig data source, NOT this
// resource — we only export the (non-secret) endpoint URLs.
var (
	_ resource.Resource                = &kubernetesClusterResource{}
	_ resource.ResourceWithConfigure   = &kubernetesClusterResource{}
	_ resource.ResourceWithImportState = &kubernetesClusterResource{}
)

// defaultK8sCreateTimeout is intentionally larger than the shared async default:
// a Kubernetes cluster provisions a CP load balancer, 1 or 3 control-plane VMs,
// kubeadm init/join, the CNI, and the default worker pool — minutes of work.
const defaultK8sCreateTimeout = 45 * 60 * 1_000_000_000 // 45 minutes, in ns

// defaultK8sUpgradeTimeout sizes the Update timeout used for a version-bump
// upgrade (T7/id-G8). A rolling CP or worker upgrade surge-replaces nodes one
// (or max_surge) at a time — the same order of magnitude of work as initial
// provisioning — so the generic defaultUpdateTimeout (10 minutes, sized for a
// simple synchronous PATCH elsewhere in this package) would be too short; this
// mirrors defaultK8sCreateTimeout instead.
const defaultK8sUpgradeTimeout = 45 * 60 * 1_000_000_000 // 45 minutes, in ns

// NewKubernetesClusterResource is the resource constructor registered with the
// provider.
func NewKubernetesClusterResource() resource.Resource {
	return &kubernetesClusterResource{}
}

// kubernetesClusterResource manages an iaas_kubernetes_cluster — a managed
// Kubernetes cluster (control plane behind a CP load balancer + a default worker
// node pool).
type kubernetesClusterResource struct {
	client *client.Client
}

// kubernetesClusterModel maps the Terraform state/plan for iaas_kubernetes_cluster.
//
// Field groups:
//   - MUTABLE metadata (name, description, project_id): updatable via PATCH.
//   - REPLACE topology inputs (slug, hypervisor_group_id, vpc_id, the subnets,
//     version, control_node_count, endpoint_mode, the plans, the optional
//     create-time tunables): immutable; changing any forces a new cluster.
//   - server-managed computed (state, endpoint_url(s), kubernetes_version,
//     worker_count).
type kubernetesClusterModel struct {
	ID types.String `tfsdk:"id"`

	// Mutable metadata.
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ProjectID   types.String `tfsdk:"project_id"`

	// Immutable topology inputs (RequiresReplace).
	Slug                 types.String `tfsdk:"slug"`
	HypervisorGroupID    types.String `tfsdk:"hypervisor_group_id"`
	VPCID                types.String `tfsdk:"vpc_id"`
	CPVPCSubnetID        types.String `tfsdk:"cp_vpc_subnet_id"`
	WorkerVPCSubnetID    types.String `tfsdk:"worker_vpc_subnet_id"`
	KubernetesVersionID  types.String `tfsdk:"kubernetes_version_id"`
	ControlNodeCount     types.Int64  `tfsdk:"control_node_count"`
	EndpointMode         types.String `tfsdk:"endpoint_mode"`
	PodCIDR              types.String `tfsdk:"pod_cidr"`
	ServiceCIDR          types.String `tfsdk:"service_cidr"`
	LBHAEnabled          types.Bool   `tfsdk:"lb_ha_enabled"`
	PodSecurityAdmission types.String `tfsdk:"pod_security_admission_default"`
	CPInstancePlanID     types.String `tfsdk:"cp_instance_plan_id"`
	CPLBPlanID           types.String `tfsdk:"cp_lb_plan_id"`
	WorkerInstancePlanID types.String `tfsdk:"worker_instance_plan_id"`
	DesiredWorkerCount   types.Int64  `tfsdk:"worker_count"`

	// Upgrade orchestration knobs (T7/id-G8) — consulted ONLY when
	// kubernetes_version_id changes; otherwise inert. Not echoed by the API, so
	// kubernetesClusterStateFromAPI always carries these straight through from
	// the plan/prior state.
	UpgradeDrainGracePeriod types.Int64 `tfsdk:"upgrade_drain_grace_period"`
	UpgradeMaxSurge         types.Int64 `tfsdk:"upgrade_max_surge"`
	UpgradeCCM              types.Bool  `tfsdk:"upgrade_ccm"`

	// Computed read-only.
	State                 types.String `tfsdk:"state"`
	KubernetesVersion     types.String `tfsdk:"kubernetes_version"`
	CPKubernetesVersionID types.String `tfsdk:"cp_kubernetes_version_id"`
	CPKubernetesVersion   types.String `tfsdk:"cp_kubernetes_version"`
	EndpointURL           types.String `tfsdk:"endpoint_url"`
	EndpointURLPublic     types.String `tfsdk:"endpoint_url_public"`
	EndpointURLPrivate    types.String `tfsdk:"endpoint_url_private"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_kubernetes_cluster".
func (r *kubernetesClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

// Schema describes the iaas_kubernetes_cluster resource.
func (r *kubernetesClusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed Kubernetes cluster: a control plane (1 or 3 nodes) behind a " +
			"dedicated CP load balancer, plus a default worker node pool. Creation is ASYNCHRONOUS " +
			"and multi-stage — the cluster row is recorded, the default pool is created, and a " +
			"provisioning job runs; this resource waits for the cluster state to become \"running\" " +
			"(created → starting → running, or \"error\" on failure). The region must have " +
			"Kubernetes + VPC + Load Balancer enabled, the VPC must have an active NAT gateway, and " +
			"the control-plane subnet must be private. name, description, project_id, and " +
			"kubernetes_version_id are mutable in place (a version_id change drives a staged in-place " +
			"upgrade — see kubernetes_version_id's description); all other topology (plans, CIDRs, " +
			"subnets, control-node count) is immutable — changing any of those forces a new cluster. " +
			"Worker scaling, node pools, kubeconfig download, and per-cluster security/SSL config are " +
			"managed by separate resources / data sources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the cluster, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// ── Mutable metadata ──────────────────────────────────────────────
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name (3-50 chars). Updatable in place (PATCH).",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Free-form description (max 500 chars). Updatable in place (PATCH).",
			},
			"project_id": schema.StringAttribute{
				Optional:    true,
				Description: "Optional project UUID to organize the cluster under. Updatable in place (PATCH).",
			},

			// ── Immutable topology inputs (RequiresReplace) ───────────────────
			"slug": schema.StringAttribute{
				Required: true,
				Description: "URL-safe slug (lowercase letters, digits, hyphens; max 63), unique within " +
					"the account. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the region (hypervisor group). Must have Kubernetes + VPC + Load " +
					"Balancer features enabled. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC the cluster lives in. Must belong to the caller, be in the " +
					"chosen region, and have an active NAT gateway with a public IP. Immutable; changing " +
					"it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cp_vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the PRIVATE VPC subnet the control plane lives in. Must belong to " +
					"the chosen VPC. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"worker_vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC subnet workers live in. Must belong to the chosen VPC. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kubernetes_version_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of an active supported Kubernetes version — the WORKER baseline (the " +
					"control plane's own current version is tracked separately, read-only, as " +
					"cp_kubernetes_version_id/cp_kubernetes_version). Updatable in place (T7/id-G8): " +
					"changing this drives a staged in-place upgrade — control plane first (if it isn't " +
					"already at the target), then workers, then (if upgrade_ccm) a CCM redeploy — rather " +
					"than replacing the cluster. The target must be an active version, forward-only, same " +
					"major, and no more than 1 minor ahead of the current baseline (server-enforced).",
			},
			"control_node_count": schema.Int64Attribute{
				Required: true,
				Description: "Number of control-plane nodes: 1 (single CP) or 3 (HA CP). Immutable; " +
					"changing it forces a new resource. lb_ha_enabled=true requires 3.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"endpoint_mode": schema.StringAttribute{
				Required: true,
				Description: "API server exposure: \"private\" (VPC-internal only) or " +
					"\"public_and_private\" (also reachable from the internet via the CP LB). Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pod_cidr": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Pod network CIDR (defaults to 10.244.0.0/16). Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"service_cidr": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Service network CIDR (defaults to 10.96.0.0/12). Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"lb_ha_enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Run the CP load balancer in HA mode (requires control_node_count=3 and an HA-capable region). Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"pod_security_admission_default": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Default PodSecurity Admission level: \"privileged\", \"baseline\", or \"restricted\" (defaults to baseline). Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cp_instance_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance plan used for control-plane nodes. Immutable; changing " +
					"it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cp_lb_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the load balancer plan used for the CP load balancer. Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"worker_instance_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance plan used for the default worker pool. Immutable " +
					"through this resource; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"worker_count": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Initial worker count for the default pool (1-50). Set at create time only " +
					"through this resource — ongoing scaling is a dedicated worker operation, so changing " +
					"this forces a new resource. The live count is also exported here on read.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
				},
			},

			// ── Upgrade orchestration knobs (T7/id-G8) ────────────────────────
			// Consulted ONLY when kubernetes_version_id changes; otherwise inert
			// (never sent to the API). Updatable in place, no RequiresReplace.
			"upgrade_drain_grace_period": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(120),
				Description: "Seconds to wait before draining each old node during a version-bump " +
					"upgrade (0-3600, default 120). Sent as drain_grace_period on BOTH the control-plane " +
					"and worker upgrade calls. Only consulted when kubernetes_version_id changes.",
			},
			"upgrade_max_surge": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(1),
				Description: "Extra workers to provision ahead of draining old ones during a version-bump " +
					"worker upgrade (>=1, default 1). Sent as max_surge on the worker upgrade call only " +
					"(the control-plane upgrade always surges 1 CP node at a time). Only consulted when " +
					"kubernetes_version_id changes.",
			},
			"upgrade_ccm": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Whether to redeploy the cloud-controller-manager (POST .../upgrade/ccm) " +
					"after a successful worker-stage upgrade, so the CCM image tracks the new worker " +
					"baseline's bundled_components.ccm_image (there is no separately-versioned ccm_version " +
					"— the API always redeploys whatever image the CURRENT worker baseline resolves to). " +
					"Only consulted when kubernetes_version_id changes; set false to manage CCM redeploys " +
					"out of band.",
			},

			// ── Computed read-only ────────────────────────────────────────────
			// state is SERVER-MUTABLE (created → starting → running → alert /
			// stopped / destroying …). Per the golden guardrail, do NOT attach
			// UseStateForUnknown to a server-mutable computed field — that would
			// mask real drift.
			"state": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle state of the cluster: \"created\", \"starting\", \"running\" " +
					"(ready), \"alert\", \"stopped\", \"error\", \"destroying\", \"destroyed\". " +
					"Server-mutable.",
			},
			"kubernetes_version": schema.StringAttribute{
				Computed: true,
				Description: "Human-readable Kubernetes semantic version (e.g. \"1.30.2\") of the WORKER " +
					"baseline (kubernetes_version_id), derived from the selected version. Server-mutable: " +
					"changes once a version-bump upgrade's worker stage completes — per the golden " +
					"guardrail, no UseStateForUnknown, so the plan doesn't mask that transition.",
			},
			"cp_kubernetes_version_id": schema.StringAttribute{
				Computed: true,
				Description: "UUID of the control plane's CURRENT Kubernetes version. Equal to " +
					"kubernetes_version_id at create; only changes when a version-bump upgrade's " +
					"control-plane stage completes (may briefly lead kubernetes_version_id during a " +
					"staged multi-minor upgrade). Server-mutable.",
			},
			"cp_kubernetes_version": schema.StringAttribute{
				Computed: true,
				Description: "Human-readable Kubernetes semantic version of cp_kubernetes_version_id. " +
					"Server-mutable.",
			},
			"endpoint_url": schema.StringAttribute{
				Computed: true,
				Description: "Stored API server endpoint URL of the cluster (the CP LB endpoint). Not a " +
					"secret. Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"endpoint_url_public": schema.StringAttribute{
				Computed: true,
				Description: "Public API server endpoint (https://<cp-lb-public-ip>), present when " +
					"endpoint_mode is public_and_private. Empty when not yet allocated. Not a secret.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"endpoint_url_private": schema.StringAttribute{
				Computed: true,
				Description: "Private (VPC) API server endpoint (https://<cp-lb-vpc-ip>). Empty when not " +
					"yet allocated. Not a secret.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// create is async (waits for state="running"); delete is async and
			// converges on SHOW→404. update is a synchronous PATCH. The create
			// default is applied as defaultK8sCreateTimeout in Create().
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *kubernetesClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create deploys the cluster and waits for it to become running:
//
//  1. A STABLE idempotency key is derived from the immutable create inputs so a
//     lost-response retry (re-applied identical config) is deduplicated by the
//     idempotency.user middleware instead of creating a second cluster.
//  2. CreateKubernetesCluster records the cluster row + default pool and returns
//     the object WITH its id (state="created") plus a tracking task_id.
//  3. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the cluster for a subsequent destroy.
//  4. WaitFor polls GetKubernetesCluster until state=="running" (fail on
//     "error"), with the LARGE create timeout (K8s is slow).
//  5. GetKubernetesCluster hydrates the computed fields; the immutable inputs are
//     echoed from the PLAN.
func (r *kubernetesClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultK8sCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":                    plan.Name.ValueString(),
		"slug":                    plan.Slug.ValueString(),
		"hypervisor_group_id":     plan.HypervisorGroupID.ValueString(),
		"vpc_id":                  plan.VPCID.ValueString(),
		"cp_vpc_subnet_id":        plan.CPVPCSubnetID.ValueString(),
		"worker_vpc_subnet_id":    plan.WorkerVPCSubnetID.ValueString(),
		"kubernetes_version_id":   plan.KubernetesVersionID.ValueString(),
		"control_node_count":      plan.ControlNodeCount.ValueInt64(),
		"endpoint_mode":           plan.EndpointMode.ValueString(),
		"cp_instance_plan_id":     plan.CPInstancePlanID.ValueString(),
		"cp_lb_plan_id":           plan.CPLBPlanID.ValueString(),
		"worker_instance_plan_id": plan.WorkerInstancePlanID.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() && plan.ProjectID.ValueString() != "" {
		body["project_id"] = plan.ProjectID.ValueString()
	}
	if !plan.PodCIDR.IsNull() && !plan.PodCIDR.IsUnknown() && plan.PodCIDR.ValueString() != "" {
		body["pod_cidr"] = plan.PodCIDR.ValueString()
	}
	if !plan.ServiceCIDR.IsNull() && !plan.ServiceCIDR.IsUnknown() && plan.ServiceCIDR.ValueString() != "" {
		body["service_cidr"] = plan.ServiceCIDR.ValueString()
	}
	if !plan.LBHAEnabled.IsNull() && !plan.LBHAEnabled.IsUnknown() {
		body["lb_ha_enabled"] = plan.LBHAEnabled.ValueBool()
	}
	if !plan.PodSecurityAdmission.IsNull() && !plan.PodSecurityAdmission.IsUnknown() && plan.PodSecurityAdmission.ValueString() != "" {
		body["pod_security_admission_default"] = plan.PodSecurityAdmission.ValueString()
	}
	if !plan.DesiredWorkerCount.IsNull() && !plan.DesiredWorkerCount.IsUnknown() {
		body["worker_count"] = plan.DesiredWorkerCount.ValueInt64()
	}

	created, err := r.client.CreateKubernetesCluster(ctx, body, idempotencyKeyForPlan(body))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating Kubernetes cluster", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster", "the create response did not include a cluster id")
		return
	}

	// Persist the id immediately so a failed provisioning/wait still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll the cluster SHOW until state="running" ────────
	// The state machine drives created → starting → running; "error" is the
	// terminal failure (recoverable out-of-band via the cluster retry endpoint).
	// Tolerance=3 absorbs transient transport blips during provisioning that
	// bypass the client's 429/5xx retry.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetKubernetesCluster(ctx, id) },
			"state",
			[]string{"running"},
			[]string{"error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for Kubernetes cluster provisioning",
			fmt.Sprintf("cluster %s did not become running: %s", id, waitErr.Error()),
		)
		return
	}

	// Read back so state reflects the endpoint URLs, version, and live worker count.
	obj, err := r.client.GetKubernetesCluster(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes cluster after provisioning", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, kubernetesClusterStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the cluster was deleted out of
// band — remove it from state so Terraform plans a recreate.
func (r *kubernetesClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetKubernetesCluster(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes cluster", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, kubernetesClusterStateFromAPI(obj, state))...)
}

// Update changes the mutable fields — name, description, project_id (a plain
// PATCH) — and, as of T7/id-G8, kubernetes_version_id (a staged in-place
// version upgrade). Everything else is RequiresReplace, so only those reach
// here.
//
// Why POST .../upgrade/retry is NOT wired into the upgrade failure path:
// verified against the Master's Kubernetes\ClusterService::retry() (the
// method UpgradeController::retryUpgrade unconditionally delegates to), that
// endpoint is gated on cluster.state=="error" and, when it runs, tears down
// EVERY partial artifact (worker + CP VMs, the CP load balancer, all three
// security groups, reserved CP IPs) and re-dispatches the FULL cluster-create
// job on the same row — a from-scratch rebuild, not a "resume the failed
// wave" operation. Neither CpRollingUpgrade nor WorkersRollingUpgrade ever
// touch cluster.state (it stays "running" throughout; only the per-task
// KubernetesClusterTask.status fails), so a failed upgrade stage would leave
// state=="running" and RetryK8sClusterUpgrade would itself 422 ("Retry is
// only available for clusters in 'error' state") rather than resume
// anything — and on a cluster that genuinely IS "error" for an unrelated
// reason, auto-calling it here would destroy/rebuild the whole cluster as a
// side effect of a version-bump apply, which is disproportionate and
// surprising. So a failed stage is surfaced as a plain Diagnostics error (the
// task/version-target columns the Master leaves behind are enough for an
// operator to diagnose and re-apply); RetryK8sClusterUpgrade remains
// available on the client for a future dedicated recovery action.
func (r *kubernetesClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		fields["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		// Send null to clear, or the new value.
		if plan.Description.IsNull() {
			fields["description"] = nil
		} else {
			fields["description"] = plan.Description.ValueString()
		}
	}
	if !plan.ProjectID.Equal(state.ProjectID) {
		if plan.ProjectID.IsNull() {
			fields["project_id"] = nil
		} else {
			fields["project_id"] = plan.ProjectID.ValueString()
		}
	}

	if len(fields) > 0 {
		// Fresh per-call idempotency key (an in-place metadata PATCH is not the
		// lost-create scenario; "" lets the client generate one).
		if _, err := r.client.UpdateKubernetesCluster(ctx, state.ID.ValueString(), fields, ""); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating Kubernetes cluster", err))
			return
		}
	}

	// ── Version upgrade (T7/id-G8): staged cp → workers → (optional) ccm ────
	if !plan.KubernetesVersionID.Equal(state.KubernetesVersionID) {
		upgradeTimeout, diags := plan.Timeouts.Update(ctx, defaultK8sUpgradeTimeout)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		if err := r.upgradeClusterVersion(ctx, state.ID.ValueString(), state.CPKubernetesVersionID.ValueString(), plan, upgradeTimeout); err != nil {
			resp.Diagnostics.AddError("Error upgrading Kubernetes cluster version", err.Error())
			return
		}
	}

	// Refresh via SHOW to rehydrate computed fields.
	obj, err := r.client.GetKubernetesCluster(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes cluster after update", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, kubernetesClusterStateFromAPI(obj, plan))...)
}

// upgradeClusterVersion drives a user-initiated kubernetes_version_id change:
// control plane first (skipped if it's already at the target — idempotent
// against a partially-applied prior attempt), then the worker baseline, then
// (if plan.UpgradeCCM) a synchronous CCM redeploy. This mirrors the Master's
// own upstream-safety invariant (WorkersUpgradeService::validateTarget rejects
// a worker target that exceeds the CP's CURRENT version — kubelet must not run
// ahead of the apiserver), and its create-time invariant that CP and worker
// baselines start equal, by driving both to the SAME target version. Staged
// CP-only or worker-only bumps to DIFFERENT targets are out of scope for this
// resource's single kubernetes_version_id knob (cp_kubernetes_version_id is
// exposed read-only for observability of any transient divergence).
func (r *kubernetesClusterResource) upgradeClusterVersion(ctx context.Context, clusterID, currentCPVersionID string, plan kubernetesClusterModel, timeout time.Duration) error {
	target := plan.KubernetesVersionID.ValueString()
	drainGrace := plan.UpgradeDrainGracePeriod.ValueInt64()
	maxSurge := plan.UpgradeMaxSurge.ValueInt64()

	// Stage 1: control plane (skip if already converged on the target).
	if currentCPVersionID != target {
		cpResp, err := r.client.UpgradeK8sClusterControlPlane(ctx, clusterID, map[string]any{
			"target_version_id":  target,
			"drain_grace_period": drainGrace,
		}, "")
		if err != nil {
			return fmt.Errorf("starting control-plane upgrade: %w", err)
		}
		if err := r.waitForClusterUpgradeTask(ctx, clusterID, taskIDFromUpgradeResponse(cpResp), timeout); err != nil {
			return fmt.Errorf("control-plane upgrade did not complete: %w", err)
		}
	}

	// Stage 2: workers (the kubernetes_version_id attribute IS the worker
	// baseline, so this stage always runs on a version-id change).
	wkResp, err := r.client.UpgradeK8sClusterWorkers(ctx, clusterID, map[string]any{
		"target_version_id":  target,
		"max_surge":          maxSurge,
		"drain_grace_period": drainGrace,
	}, "")
	if err != nil {
		return fmt.Errorf("starting worker upgrade: %w", err)
	}
	if err := r.waitForClusterUpgradeTask(ctx, clusterID, taskIDFromUpgradeResponse(wkResp), timeout); err != nil {
		return fmt.Errorf("worker upgrade did not complete: %w", err)
	}

	// Stage 3: CCM redeploy — SYNCHRONOUS (no task_id, no wait needed). The
	// image is resolved server-side from the (now-updated) worker baseline's
	// bundled_components.ccm_image.
	if plan.UpgradeCCM.ValueBool() {
		if err := r.client.UpgradeK8sClusterCCM(ctx, clusterID, ""); err != nil {
			return fmt.Errorf("redeploying CCM: %w", err)
		}
	}

	return nil
}

// waitForClusterUpgradeTask polls the cluster SHOW and scans its embedded
// "tasks" array (eager-loaded, last 20, newest first) for taskID, waiting for
// THAT task's own "status" field to reach "completed" (fail: "failed"). There
// is no per-task GET route for clusters (unlike instances'
// GetInstanceTask/waiter pair), so this reuses the cluster's existing
// convergence primitive — waiter.WaitFor + a Refresh closure over
// GetKubernetesCluster — the SAME pattern Create/Delete already use for
// "state" convergence, just classifying a different embedded field.
func (r *kubernetesClusterResource) waitForClusterUpgradeTask(ctx context.Context, clusterID, taskID string, timeout time.Duration) error {
	if taskID == "" {
		// Defensively treat a missing task_id (e.g. a future no-op
		// short-circuit response) as already converged rather than hanging.
		return nil
	}
	return waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  timeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) {
				obj, err := r.client.GetKubernetesCluster(ctx, clusterID)
				if err != nil {
					return nil, err
				}
				tasks, _ := obj["tasks"].([]any)
				for _, t := range tasks {
					task, ok := t.(map[string]any)
					if !ok {
						continue
					}
					if id, _ := task["id"].(string); id == taskID {
						return task, nil
					}
				}
				// Not (yet) visible in the last-20 window — keep polling; the
				// missing "status" key classifies as "keep polling" below.
				return map[string]any{}, nil
			},
			"status",
			[]string{"completed"},
			[]string{"failed"},
			3,
		),
	})
}

// taskIDFromUpgradeResponse extracts "task_id" from an upgrade/cp or
// upgrade/workers response (the bare {task_id,target_version_id,...} envelope).
func taskIDFromUpgradeResponse(obj map[string]any) string {
	id, _ := obj["task_id"].(string)
	return id
}

// Delete removes the cluster. DELETE marks it "destroying", dispatches the
// teardown job (worker VMs, CP load balancer, security groups, reserved IPs),
// and soft-deletes the row, so a subsequent SHOW 404s. We poll until SHOW 404s
// (IsNotFound) as the convergence signal, with the LARGE delete timeout. An
// already-destroyed cluster surfaces as success:false from
// DeleteKubernetesCluster.
func (r *kubernetesClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, defaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.client.DeleteKubernetesCluster(ctx, id, ""); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting Kubernetes cluster", err))
		return
	}

	// Converge by polling SHOW until it 404s. The Refresh closure treats an
	// IsNotFound error as "done", and any other error as terminal.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetKubernetesCluster(ctx, id)
			if err != nil {
				if client.IsNotFound(err) {
					return "destroyed", true, nil
				}
				return "", false, err
			}
			return "destroying", false, nil
		},
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for Kubernetes cluster deletion",
			fmt.Sprintf("cluster %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState lets `terraform import iaas_kubernetes_cluster.x <uuid>` adopt an
// existing cluster; the next Read populates the readable attributes.
func (r *kubernetesClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// kubernetesClusterStateFromAPI builds the model from a SHOW cluster object. The
// immutable inputs authoritative value is the plan/state; the computed fields
// come from the API. kubernetes_version is extracted from the nested
// kubernetes_version{semantic_version} object.
func kubernetesClusterStateFromAPI(obj map[string]any, prior kubernetesClusterModel) kubernetesClusterModel {
	return kubernetesClusterModel{
		ID: stringFromAPI(obj, "id", prior.ID),

		// Mutable metadata.
		Name:        stringFromAPI(obj, "name", prior.Name),
		Description: optionalStringFromAPI(obj, "description", prior.Description),
		ProjectID:   optionalStringFromAPI(obj, "project_id", prior.ProjectID),

		// Immutable topology inputs — authoritative value is the plan; SHOW echoes them.
		Slug:                stringOrPrior(obj, "slug", prior.Slug),
		HypervisorGroupID:   stringOrPrior(obj, "hypervisor_group_id", prior.HypervisorGroupID),
		VPCID:               stringOrPrior(obj, "vpc_id", prior.VPCID),
		CPVPCSubnetID:       stringOrPrior(obj, "cp_vpc_subnet_id", prior.CPVPCSubnetID),
		WorkerVPCSubnetID:   stringOrPrior(obj, "worker_vpc_subnet_id", prior.WorkerVPCSubnetID),
		KubernetesVersionID: stringOrPrior(obj, "kubernetes_version_id", prior.KubernetesVersionID),
		ControlNodeCount:    int64FromAPI(obj, "control_node_count", prior.ControlNodeCount),
		EndpointMode:        stringOrPrior(obj, "endpoint_mode", prior.EndpointMode),
		// pod_cidr / service_cidr / pod_security_admission_default are
		// Optional+Computed: the server stores and echoes them (with defaults
		// when omitted). computedStringFromAPI settles an absent value to a known
		// "" so the Computed contract (known-after-apply) holds even if a future
		// SHOW drops the field.
		PodCIDR:              computedStringFromAPI(obj, "pod_cidr", prior.PodCIDR),
		ServiceCIDR:          computedStringFromAPI(obj, "service_cidr", prior.ServiceCIDR),
		LBHAEnabled:          boolFromIntAPI(obj, "lb_ha_enabled", prior.LBHAEnabled),
		PodSecurityAdmission: computedStringFromAPI(obj, "pod_security_admission_default", prior.PodSecurityAdmission),
		CPInstancePlanID:     stringOrPrior(obj, "cp_instance_plan_id", prior.CPInstancePlanID),
		CPLBPlanID:           stringOrPrior(obj, "cp_lb_plan_id", prior.CPLBPlanID),
		WorkerInstancePlanID: stringOrPrior(obj, "worker_instance_plan_id", prior.WorkerInstancePlanID),
		DesiredWorkerCount:   int64FromAPI(obj, "worker_count", prior.DesiredWorkerCount),

		// Upgrade orchestration knobs — never echoed by the API; carried
		// straight through from the plan (Create/Update) or prior state (Read).
		UpgradeDrainGracePeriod: prior.UpgradeDrainGracePeriod,
		UpgradeMaxSurge:         prior.UpgradeMaxSurge,
		UpgradeCCM:              prior.UpgradeCCM,

		// Computed read-only.
		State:                 stringFromAPI(obj, "state", prior.State),
		KubernetesVersion:     nestedStringFromAPI(obj, "kubernetes_version", "semantic_version", prior.KubernetesVersion),
		CPKubernetesVersionID: computedStringFromAPI(obj, "cp_kubernetes_version_id", prior.CPKubernetesVersionID),
		CPKubernetesVersion:   nestedStringFromAPI(obj, "cp_kubernetes_version", "semantic_version", prior.CPKubernetesVersion),
		EndpointURL:           computedStringFromAPI(obj, "endpoint_url", prior.EndpointURL),
		EndpointURLPublic:     computedStringFromAPI(obj, "endpoint_url_public", prior.EndpointURLPublic),
		EndpointURLPrivate:    computedStringFromAPI(obj, "endpoint_url_private", prior.EndpointURLPrivate),

		Timeouts: prior.Timeouts,
	}
}

// idempotencyKeyForPlan derives a STABLE idempotency key from the create body so
// a re-applied identical config reuses the same key. The Master's
// idempotency.user middleware then replays its cached 2xx response (for 24h)
// instead of provisioning a SECOND cluster when a create's HTTP response was
// lost but the server-side create succeeded.
//
// The key is a sha256 over the body's key=value pairs in sorted-key order
// (deterministic regardless of Go map iteration order), prefixed so it is
// recognisably a provider-generated key. Two genuinely different configs hash to
// different keys (no false dedup); two identical configs hash to the same key
// (safe dedup).
func idempotencyKeyForPlan(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%v;", k, body[k])
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return "tf-k8s-" + hex.EncodeToString(sum[:])
}
