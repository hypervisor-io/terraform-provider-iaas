package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions — iaas_kubernetes_node_pool is a CHILD resource of
// iaas_kubernetes_cluster. It copies the golden child recipe (vpc_subnet):
//   - the parent cluster UUID lives in the URL path (cluster_id, RequiresReplace),
//   - import takes a COMPOSITE id "<cluster_id>/<pool_id>",
//   - the read uses LIST-and-match (the user API has NO per-pool SHOW endpoint),
//   - server-mutable computed fields (is_default, current_node_count) OMIT
//     UseStateForUnknown (the guardrail).
//
// SYNC, no waiter: createPool inserts the pool row in a transaction and returns
// it synchronously (HTTP 201 with id). Worker provisioning is dispatched
// fire-and-forget and there is NO per-pool status/state column to poll — exactly
// like vpc_subnet's async IP generation with no status field. So Create is
// synchronous (no timeouts block, no waiter); the live worker count surfaces on
// later reads via the LIST's vm_refs_count.
//
// Actions NOT modelled: reassign (promote to default — mutates the server-managed
// is_default flag) and cancel-pending (clear a single node's deletion marker).
// Both are operational, not declarative IaC state, so neither is exposed.
var (
	_ resource.Resource                = &kubernetesNodePoolResource{}
	_ resource.ResourceWithConfigure   = &kubernetesNodePoolResource{}
	_ resource.ResourceWithImportState = &kubernetesNodePoolResource{}
)

// NewKubernetesNodePoolResource is the resource constructor registered with the
// provider.
func NewKubernetesNodePoolResource() resource.Resource {
	return &kubernetesNodePoolResource{}
}

// kubernetesNodePoolResource manages an iaas_kubernetes_node_pool — an additional
// worker node pool on a managed Kubernetes cluster, each backed by its own
// instance plan, sizing bounds, labels, taints and autoscaling config.
type kubernetesNodePoolResource struct {
	client *client.Client
}

// taintAttrTypes is the object schema for a single node taint, reused for the
// types.List construction in the state mapper.
var taintAttrTypes = map[string]attr.Type{
	"key":    types.StringType,
	"value":  types.StringType,
	"effect": types.StringType,
}

// kubernetesNodePoolModel maps the Terraform state/plan for
// iaas_kubernetes_node_pool.
//
// Field groups:
//   - parent (cluster_id): in the URL path → Required + RequiresReplace.
//   - create inputs, all MUTABLE in place via PATCH: name, instance_plan_id,
//     min_size, max_size, target_count, weight, autoscaling_enabled, labels,
//     taints.
//   - server-managed computed (is_default, current_node_count): server-mutable →
//     NO UseStateForUnknown.
type kubernetesNodePoolModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`

	// Mutable create inputs.
	Name               types.String `tfsdk:"name"`
	InstancePlanID     types.String `tfsdk:"instance_plan_id"`
	MinSize            types.Int64  `tfsdk:"min_size"`
	MaxSize            types.Int64  `tfsdk:"max_size"`
	TargetCount        types.Int64  `tfsdk:"target_count"`
	Weight             types.Int64  `tfsdk:"weight"`
	AutoscalingEnabled types.Bool   `tfsdk:"autoscaling_enabled"`
	Labels             types.Map    `tfsdk:"labels"`
	Taints             types.List   `tfsdk:"taints"`

	// Server-managed computed.
	IsDefault        types.Bool  `tfsdk:"is_default"`
	CurrentNodeCount types.Int64 `tfsdk:"current_node_count"`
}

// Metadata sets the resource type name → "<provider>_kubernetes_node_pool".
func (r *kubernetesNodePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

// Schema describes the iaas_kubernetes_node_pool resource.
func (r *kubernetesNodePoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a worker node pool on a managed Kubernetes cluster. A node " +
			"pool is a child of a cluster: its parent cluster_id is part of the API path, so changing " +
			"it forces a new resource. Each pool is backed by its own instance plan and carries its own " +
			"sizing bounds (min_size/max_size), desired worker count (target_count), labels, taints and " +
			"autoscaling toggle. Creation is synchronous (the pool row is recorded immediately); worker " +
			"VMs are then provisioned asynchronously in the background, so the live worker count " +
			"(current_node_count) populates on later reads. The first pool on a cluster is auto-promoted " +
			"to the default (is_default); promoting a different pool to default and cancelling pending " +
			"node deletions are operational actions handled outside Terraform. This resource ALSO manages " +
			"the cluster's DEFAULT worker pool (is_default=true): import the default pool by its " +
			"\"<cluster_id>/<pool_id>\" id and manage its scale (target_count), labels/taints and " +
			"autoscaling (min_size/max_size/autoscaling_enabled) in place. The cluster-level worker " +
			"endpoints (workers/scale, workers/labels, workers/autoscaling) are deprecated backward-" +
			"compat shims that resolve the default pool and delegate to the same per-pool service this " +
			"resource drives, so they are not modelled separately. NOTE: for user-driven " +
			"edits the server keeps min_size and target_count in lockstep — set them to the same value " +
			"(supplying only one mirrors it to the other).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the node pool, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent Kubernetes cluster this pool belongs to. This value is " +
					"part of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// ── Mutable create inputs ─────────────────────────────────────────
			"name": schema.StringAttribute{
				Required: true,
				Description: "DNS-1123-style pool name (lowercase letters, digits and hyphens; must " +
					"start with a letter and end alphanumeric; max 64), unique within the cluster. " +
					"Updatable in place.",
			},
			"instance_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance plan backing the workers in this pool. Updatable in " +
					"place, but ONLY while the pool has no live workers — the API rejects a plan change " +
					"on a pool with running nodes (scale the pool to zero first).",
			},
			"min_size": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Autoscaling minimum (0-1000, default 0). For user edits the server keeps this equal to target_count. Updatable in place.",
			},
			"max_size": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(3),
				Description: "Autoscaling maximum (1-1000, default 3; must be >= min_size). Updatable in place.",
			},
			"target_count": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				Description: "Desired worker count for this pool (0-1000, default 0). Changing it scales " +
					"the pool — workers are actually provisioned or drained. For user edits the server " +
					"keeps this equal to min_size. Updatable in place.",
			},
			"weight": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(50),
				Description: "Scaler weight when several pools are eligible to grow (1-100, default 50; higher = preferred). Updatable in place.",
			},
			"autoscaling_enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the cluster-autoscaler may scale this pool between min_size and max_size (default true). Updatable in place.",
			},
			"labels": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Kubernetes node labels applied to every node in this pool, as a string→string " +
					"map. Updating sends the full replacement set. Omit to apply no custom labels.",
			},
			"taints": schema.ListNestedAttribute{
				Optional: true,
				Description: "Kubernetes node taints applied to every node in this pool. Updating sends " +
					"the full replacement list. Each entry sets key, optional value, and effect.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Required:    true,
							Description: "Taint key.",
						},
						"value": schema.StringAttribute{
							Optional:    true,
							Description: "Taint value (optional).",
						},
						"effect": schema.StringAttribute{
							Required:    true,
							Description: "Taint effect: \"NoSchedule\", \"PreferNoSchedule\", or \"NoExecute\".",
						},
					},
				},
			},

			// ── Server-managed computed ───────────────────────────────────────
			// is_default and current_node_count are SERVER-MUTABLE: is_default
			// flips via the reassign action (not modelled) and the first-pool
			// auto-promotion; current_node_count tracks live worker vm_refs as
			// they provision/drain. Per the golden guardrail, do NOT attach
			// UseStateForUnknown to server-mutable computed fields (it would mask
			// real drift).
			"is_default": schema.BoolAttribute{
				Computed: true,
				Description: "Whether this is the cluster's default pool. The first pool on a cluster is " +
					"auto-promoted; reassigning the default is an operational action outside Terraform. " +
					"Server-mutable.",
			},
			"current_node_count": schema.Int64Attribute{
				Computed: true,
				Description: "Live count of worker nodes (vm_refs) currently in this pool. Populated " +
					"asynchronously as workers provision/drain. Server-mutable.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *kubernetesNodePoolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions a node pool under its parent cluster. The create is
// synchronous — the response carries the pool with its id, which we read back so
// state reflects the server-applied defaults (min/max/target, is_default) and the
// (initially empty) live node count. A STABLE idempotency key derived from the
// immutable create inputs makes a lost-response retry safe.
func (r *kubernetesNodePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := nodePoolCreateBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateKubernetesNodePool(ctx, plan.ClusterID.ValueString(), body, idempotencyKeyForNodePool(plan.ClusterID.ValueString(), body))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating Kubernetes node pool", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating Kubernetes node pool", "the create response did not include a pool id")
		return
	}

	// Persist the id immediately so a failed read-back still tracks the resource
	// for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read back via LIST-and-match so state reflects server-applied defaults and
	// the lockstep min_size/target_count mirroring.
	obj, err := r.client.GetKubernetesNodePool(ctx, plan.ClusterID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes node pool after creation", err))
		return
	}

	state, mapDiags := kubernetesNodePoolStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API via LIST-and-match (no per-pool SHOW). A
// not-found (pool absent from the list, or the parent cluster 404s) removes the
// resource from state so Terraform plans a recreate.
func (r *kubernetesNodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetKubernetesNodePool(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes node pool", err))
		return
	}

	next, mapDiags := kubernetesNodePoolStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Update patches the mutable fields (name, instance_plan_id, sizing, weight,
// autoscaling_enabled, labels, taints). Only changed fields are sent. The PATCH
// response carries the updated pool, but we read back via LIST so the lockstep
// min_size/target_count mirroring and any background scale settle into state.
func (r *kubernetesNodePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state kubernetesNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields, diags := nodePoolUpdateBody(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(fields) > 0 {
		// Fresh per-call idempotency key (an in-place PATCH is not the lost-create
		// scenario; "" lets the client generate one).
		if _, err := r.client.UpdateKubernetesNodePool(ctx, plan.ClusterID.ValueString(), plan.ID.ValueString(), fields, ""); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating Kubernetes node pool", err))
			return
		}
	}

	obj, err := r.client.GetKubernetesNodePool(ctx, plan.ClusterID.ValueString(), plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading Kubernetes node pool after update", err))
		return
	}

	next, mapDiags := kubernetesNodePoolStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Delete removes the pool. The client sends force=true so the workers are
// cordoned/drained/deleted synchronously and `terraform destroy` is
// deterministic. The last/default pool cannot be deleted (the API rejects it);
// reassign the default to another pool first.
func (r *kubernetesNodePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteKubernetesNodePool(ctx, state.ClusterID.ValueString(), state.ID.ValueString(), ""); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting Kubernetes node pool", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource. Because the
// parent cluster_id is required to build the API path (and is not derivable from
// the pool id alone), `terraform import` must supply BOTH ids joined by a slash:
//
//	terraform import iaas_kubernetes_node_pool.x <cluster_id>/<pool_id>
//
// We split req.ID on the FIRST "/" into cluster_id and pool_id; the subsequent
// Read (LIST-and-match) hydrates the remaining attributes.
func (r *kubernetesNodePoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, poolID, ok := strings.Cut(req.ID, "/")
	if !ok || clusterID == "" || poolID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"cluster_id/pool_id\", got: %q. "+
				"Node pools are child resources, so both the parent cluster id and the "+
				"pool id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), poolID)...)
}

// nodePoolCreateBody builds the create request body from the plan, omitting unset
// optionals so server defaults apply. labels/taints are converted to their API
// shapes; Optional+Computed numeric fields always have a (defaulted) known value
// so they are always sent.
func nodePoolCreateBody(ctx context.Context, plan kubernetesNodePoolModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics

	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"instance_plan_id":    plan.InstancePlanID.ValueString(),
		"min_size":            plan.MinSize.ValueInt64(),
		"max_size":            plan.MaxSize.ValueInt64(),
		"target_count":        plan.TargetCount.ValueInt64(),
		"weight":              plan.Weight.ValueInt64(),
		"autoscaling_enabled": plan.AutoscalingEnabled.ValueBool(),
	}

	if !plan.Labels.IsNull() && !plan.Labels.IsUnknown() {
		labels, d := labelsToAPIMap(ctx, plan.Labels)
		diags.Append(d...)
		if !diags.HasError() {
			body["labels"] = labels
		}
	}
	if !plan.Taints.IsNull() && !plan.Taints.IsUnknown() {
		taints, d := taintsToAPI(ctx, plan.Taints)
		diags.Append(d...)
		if !diags.HasError() {
			body["taints"] = taints
		}
	}
	return body, diags
}

// nodePoolUpdateBody builds the PATCH body containing only the fields that
// changed between state and plan. labels/taints are sent in full (the API
// replaces the whole set) whenever they differ.
func nodePoolUpdateBody(ctx context.Context, plan, state kubernetesNodePoolModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	fields := map[string]any{}

	if !plan.Name.Equal(state.Name) {
		fields["name"] = plan.Name.ValueString()
	}
	if !plan.InstancePlanID.Equal(state.InstancePlanID) {
		fields["instance_plan_id"] = plan.InstancePlanID.ValueString()
	}
	if !plan.MinSize.Equal(state.MinSize) {
		fields["min_size"] = plan.MinSize.ValueInt64()
	}
	if !plan.MaxSize.Equal(state.MaxSize) {
		fields["max_size"] = plan.MaxSize.ValueInt64()
	}
	if !plan.TargetCount.Equal(state.TargetCount) {
		fields["target_count"] = plan.TargetCount.ValueInt64()
	}
	if !plan.Weight.Equal(state.Weight) {
		fields["weight"] = plan.Weight.ValueInt64()
	}
	if !plan.AutoscalingEnabled.Equal(state.AutoscalingEnabled) {
		fields["autoscaling_enabled"] = plan.AutoscalingEnabled.ValueBool()
	}
	if !plan.Labels.Equal(state.Labels) {
		// Send the full replacement set (null/empty → {} to clear).
		labels, d := labelsToAPIMap(ctx, plan.Labels)
		diags.Append(d...)
		if !diags.HasError() {
			fields["labels"] = labels
		}
	}
	if !plan.Taints.Equal(state.Taints) {
		taints, d := taintsToAPI(ctx, plan.Taints)
		diags.Append(d...)
		if !diags.HasError() {
			fields["taints"] = taints
		}
	}
	return fields, diags
}

// labelsToAPIMap converts the labels types.Map(string) to map[string]any. A
// null/unknown map yields an empty map so the API receives {} (clear) rather than
// omitting the field on update.
func labelsToAPIMap(ctx context.Context, m types.Map) (map[string]any, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return map[string]any{}, nil
	}
	var goMap map[string]string
	d := m.ElementsAs(ctx, &goMap, false)
	if d.HasError() {
		return nil, d
	}
	out := make(map[string]any, len(goMap))
	for k, v := range goMap {
		out[k] = v
	}
	return out, nil
}

// taintModel is the Go representation of a single taint list element.
type taintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

// taintsToAPI converts the taints types.List to a []map[string]any for the API
// (each {key, value?, effect}). A null/unknown list yields an empty slice so the
// API receives [] (clear) on update.
func taintsToAPI(ctx context.Context, l types.List) ([]map[string]any, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return []map[string]any{}, nil
	}
	var items []taintModel
	d := l.ElementsAs(ctx, &items, false)
	if d.HasError() {
		return nil, d
	}
	out := make([]map[string]any, 0, len(items))
	for _, t := range items {
		entry := map[string]any{
			"key":    t.Key.ValueString(),
			"effect": t.Effect.ValueString(),
		}
		if !t.Value.IsNull() && !t.Value.IsUnknown() {
			entry["value"] = t.Value.ValueString()
		}
		out = append(out, entry)
	}
	return out, nil
}

// kubernetesNodePoolStateFromAPI builds the model from an API pool object
// (returned by create or the LIST scan). cluster_id is never in the body (it is
// in the path), so it falls back to the prior plan/state value. labels/taints are
// rebuilt from the embedded JSON; the live node count comes from vm_refs_count.
func kubernetesNodePoolStateFromAPI(ctx context.Context, obj map[string]any, prior kubernetesNodePoolModel) (kubernetesNodePoolModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	labels, d := apiToLabelsMap(obj["labels"])
	diags.Append(d...)
	taints, d2 := apiToTaintsList(obj["taints"])
	diags.Append(d2...)

	m := kubernetesNodePoolModel{
		ID:        stringFromAPI(obj, "id", prior.ID),
		ClusterID: prior.ClusterID, // never in the response body; from the path

		Name:               stringFromAPI(obj, "name", prior.Name),
		InstancePlanID:     stringFromAPI(obj, "instance_plan_id", prior.InstancePlanID),
		MinSize:            int64FromAPI(obj, "min_size", prior.MinSize),
		MaxSize:            int64FromAPI(obj, "max_size", prior.MaxSize),
		TargetCount:        int64FromAPI(obj, "target_count", prior.TargetCount),
		Weight:             int64FromAPI(obj, "weight", prior.Weight),
		AutoscalingEnabled: boolFromIntAPI(obj, "autoscaling_enabled", prior.AutoscalingEnabled),
		Labels:             labels,
		Taints:             taints,

		IsDefault:        boolFromIntAPI(obj, "is_default", prior.IsDefault),
		CurrentNodeCount: int64FromAPI(obj, "vm_refs_count", prior.CurrentNodeCount),
	}
	return m, diags
}

// apiToLabelsMap converts the API "labels" field (a map[string]any or null) to a
// types.Map(string). An absent/null/empty value yields a null map so an omitted
// labels block round-trips without spurious drift (labels is Optional, not
// Computed).
func apiToLabelsMap(raw any) (types.Map, diag.Diagnostics) {
	apiMap, ok := raw.(map[string]any)
	if !ok || len(apiMap) == 0 {
		return types.MapNull(types.StringType), nil
	}
	elems := make(map[string]attr.Value, len(apiMap))
	for k, v := range apiMap {
		var s string
		switch tv := v.(type) {
		case string:
			s = tv
		case nil:
			s = ""
		default:
			s = fmt.Sprintf("%v", tv)
		}
		elems[k] = types.StringValue(s)
	}
	return types.MapValue(types.StringType, elems)
}

// apiToTaintsList converts the API "taints" field (a []any of {key,value?,effect}
// or null) to a types.List of taint objects. An absent/null/empty value yields a
// null list so an omitted taints block round-trips without spurious drift.
func apiToTaintsList(raw any) (types.List, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: taintAttrTypes}

	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return types.ListNull(objType), nil
	}

	var diags diag.Diagnostics
	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := t["key"].(string)
		effect, _ := t["effect"].(string)
		valueAttr := types.StringNull()
		if v, ok := t["value"].(string); ok && v != "" {
			valueAttr = types.StringValue(v)
		}
		obj, d := types.ObjectValue(taintAttrTypes, map[string]attr.Value{
			"key":    types.StringValue(key),
			"value":  valueAttr,
			"effect": types.StringValue(effect),
		})
		diags.Append(d...)
		elems = append(elems, obj)
	}
	if len(elems) == 0 {
		return types.ListNull(objType), diags
	}
	list, d := types.ListValue(objType, elems)
	diags.Append(d...)
	return list, diags
}

// idempotencyKeyForNodePool derives a STABLE idempotency key from the parent
// cluster id + the create body so a re-applied identical config reuses the same
// key. The Master's idempotency.user middleware then replays its cached 2xx
// response (for 24h) instead of creating a SECOND pool when a create's HTTP
// response was lost but the server-side create succeeded.
//
// The key is a sha256 over the cluster id + the body's key=value pairs in
// sorted-key order (deterministic regardless of Go map iteration order). Two
// genuinely different configs hash to different keys (no false dedup).
func idempotencyKeyForNodePool(clusterID string, body map[string]any) string {
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
	return "tf-k8s-pool-" + hex.EncodeToString(sum[:])
}
