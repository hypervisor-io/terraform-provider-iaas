package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions — iaas_alert_rule is a simple sync resource.
var (
	_ resource.Resource                = &alertRuleResource{}
	_ resource.ResourceWithConfigure   = &alertRuleResource{}
	_ resource.ResourceWithImportState = &alertRuleResource{}
)

// NewAlertRuleResource is the resource constructor registered with the provider.
func NewAlertRuleResource() resource.Resource {
	return &alertRuleResource{}
}

// alertRuleResource manages an iaas_alert_rule — a metric-threshold alert rule
// that fires through attached notification channels when a breach persists for
// the configured duration.
//
// Route summary (verified against UserApi\AlertRuleController + StoreRequest +
// UpdateRequest + AlertRule model + routes/user_api.php):
//
//	LIST    GET    /alert-rules                (PLURAL)
//	                → {success,alert_rules:{current_page,data:[...],total,...}}
//	CREATE  POST   /alert-rules                (PLURAL)
//	                body {name (required,max:255),
//	                      resource_type (required,in:instance|managed_database|
//	                                     load_balancer|vpn_gateway),
//	                      resource_id   (nullable|uuid),
//	                      metric        (required|string),
//	                      operator      (required,in:gt|lt|gte|lte|eq),
//	                      threshold     (required|numeric),
//	                      duration      (nullable|integer|min:0),
//	                      reminder_interval (nullable|integer|min:0),
//	                      channel_ids   (nullable|array of UUIDs)}
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	SHOW    GET    /alert-rule/{id}            (SINGULAR)
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	UPDATE  PATCH  /alert-rule/{id}            (SINGULAR)
//	                body same as CREATE plus enabled (sometimes|boolean)
//	                channel_ids replaces the set via channels().sync()
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	DELETE  DELETE /alert-rule/{id}            (SINGULAR)
//	                → {success}
//
// Key design decisions:
//   - channel_ids is modelled as a SetAttribute(String). The controller accepts
//     a full desired set and syncs (replaces) the attachment server-side, so the
//     resource always sends the full set without needing separate attach/detach
//     calls (unlike security_group which has dedicated attach/detach endpoints).
//   - resource_id is Optional (nullable UUID) — when omitted the rule applies to
//     all resources of resource_type owned by the account.
//   - All scalar fields (name, resource_type, resource_id, metric, operator,
//     threshold, duration, reminder_interval, enabled) are updatable in place;
//     none require replacement.
//   - enabled is Optional+Computed: the server defaults to 1 (true) on create.
//   - status ("ok"/"firing") is server-mutable Computed WITHOUT UseStateForUnknown
//     — it changes whenever the rule fires or resolves and must always reflect the
//     refreshed server value.
//   - acknowledge (POST /alert-rule/{id}/acknowledge) is an operational action,
//     NOT modelled as IaC state.
//   - All operations are SYNCHRONOUS (no async task/waiter).
//   - Routes are gated by subuser permissions: monitoring.view (LIST/SHOW) and
//     monitoring.manage (CREATE/UPDATE/DELETE).
type alertRuleResource struct {
	client *client.Client
}

// alertRuleModel maps the Terraform state/plan for iaas_alert_rule.
type alertRuleModel struct {
	ID               types.String  `tfsdk:"id"`
	Name             types.String  `tfsdk:"name"`
	ResourceType     types.String  `tfsdk:"resource_type"`
	ResourceID       types.String  `tfsdk:"resource_id"`
	Metric           types.String  `tfsdk:"metric"`
	Operator         types.String  `tfsdk:"operator"`
	Threshold        types.Float64 `tfsdk:"threshold"`
	Duration         types.Int64   `tfsdk:"duration"`
	ReminderInterval types.Int64   `tfsdk:"reminder_interval"`
	ChannelIDs       types.Set     `tfsdk:"channel_ids"`
	Enabled          types.Bool    `tfsdk:"enabled"`
	Status           types.String  `tfsdk:"status"`
}

// Metadata sets the resource type name → "<provider>_alert_rule".
func (r *alertRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_rule"
}

// Schema describes the iaas_alert_rule resource.
func (r *alertRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a metric-threshold alert rule that fires through attached notification " +
			"channels when a metric on a resource (or all resources of a type) crosses a threshold " +
			"for the configured duration.\n\n" +
			"The `acknowledge` action (POST /alert-rule/{id}/acknowledge) is an operational helper " +
			"and is not modelled as Terraform state.\n\n" +
			"Routes are gated by subuser permissions: `monitoring.view` for read operations, " +
			"`monitoring.manage` for write operations.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the alert rule, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Friendly label for the alert rule. Maximum 255 characters. Updatable in place.",
			},
			"resource_type": schema.StringAttribute{
				Required: true,
				Description: "The type of resource this rule monitors. One of: `instance`, " +
					"`managed_database`, `load_balancer`, `vpn_gateway`. Updatable in place.",
			},
			"resource_id": schema.StringAttribute{
				Optional: true,
				Description: "UUID of a specific resource to monitor. When omitted, the rule " +
					"applies to every resource of the given `resource_type` owned by the account. " +
					"Updatable in place.",
			},
			"metric": schema.StringAttribute{
				Required: true,
				Description: "Metric key to monitor, e.g. `cpu_pct`, `mem_pct`, `disk_pct`, " +
					"`bandwidth_in`, `bandwidth_out`. Updatable in place.",
			},
			"operator": schema.StringAttribute{
				Required: true,
				Description: "Comparison operator: `gt` (>), `lt` (<), `gte` (>=), `lte` (<=), " +
					"or `eq` (=). Updatable in place.",
			},
			"threshold": schema.Float64Attribute{
				Required: true,
				Description: "Numeric threshold value. The rule fires when `metric operator threshold` " +
					"evaluates to true for at least `duration` seconds. Updatable in place.",
			},
			"duration": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Seconds the threshold breach must persist before the rule fires. " +
					"0 means fire immediately. Defaults to 0 when omitted. Updatable in place.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"reminder_interval": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Seconds between repeated firings while the breach is still active. " +
					"0 means no reminders. Defaults to 0 when omitted. Updatable in place.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"channel_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "UUIDs of the notification channels to dispatch through, as an " +
					"order-independent set. On each update the full desired set is sent; the " +
					"server replaces the attached channels via sync. Omit or set to empty to " +
					"detach all channels.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether the alert rule is enabled. Defaults to true when omitted. " +
					"Set to false to disable evaluation without deleting the rule.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Current firing state as reported by the server: `ok` (threshold not " +
					"breached) or `firing` (threshold breached and channels have been notified). " +
					"Server-managed; changes when the rule fires or resolves.",
				// Server-mutable: do NOT add UseStateForUnknown. The server can change
				// this field at any time; the plan must always show the refreshed value.
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. Tolerates a nil
// ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *alertRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the alert rule with the configured metric/threshold/channels.
// The API returns the new rule with its id.
func (r *alertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := alertRuleToBody(ctx, plan)

	obj, err := r.client.CreateAlertRule(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating alert rule", err))
		return
	}

	state := alertRuleFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the rule was deleted out of
// band — remove it from state so Terraform plans a recreate.
func (r *alertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetAlertRule(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading alert rule", err))
		return
	}

	newState := alertRuleFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update applies the planned changes. All fields are updatable in place.
// channel_ids is sent as the full desired set; the controller syncs channels.
func (r *alertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := alertRuleToBody(ctx, plan)

	obj, err := r.client.UpdateAlertRule(ctx, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating alert rule", err))
		return
	}

	newState := alertRuleFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the alert rule. The controller detaches all channels
// before deletion.
func (r *alertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteAlertRule(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting alert rule", err))
		return
	}
}

// ImportState lets `terraform import iaas_alert_rule.x <uuid>` adopt an existing
// rule; the next Read populates the rest of the attributes.
func (r *alertRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// alertRuleToBody builds the Create/Update request body from a planned model.
// Optional fields are omitted when null/unknown so the controller uses its
// defaults (duration=0, reminder_interval=0, enabled=true).
func alertRuleToBody(ctx context.Context, plan alertRuleModel) map[string]any {
	body := map[string]any{
		"name":          plan.Name.ValueString(),
		"resource_type": plan.ResourceType.ValueString(),
		"metric":        plan.Metric.ValueString(),
		"operator":      plan.Operator.ValueString(),
		"threshold":     plan.Threshold.ValueFloat64(),
	}

	if !plan.ResourceID.IsNull() && !plan.ResourceID.IsUnknown() {
		body["resource_id"] = plan.ResourceID.ValueString()
	}
	if !plan.Duration.IsNull() && !plan.Duration.IsUnknown() {
		body["duration"] = plan.Duration.ValueInt64()
	}
	if !plan.ReminderInterval.IsNull() && !plan.ReminderInterval.IsUnknown() {
		body["reminder_interval"] = plan.ReminderInterval.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	// channel_ids: always send when the attribute is set (even to empty, so the
	// controller syncs to zero channels). Null/unknown means "don't touch" — omit
	// so the controller leaves the current channel set unchanged.
	if !plan.ChannelIDs.IsNull() && !plan.ChannelIDs.IsUnknown() {
		ids := make([]string, 0, len(plan.ChannelIDs.Elements()))
		for _, v := range plan.ChannelIDs.Elements() {
			if s, ok := v.(types.String); ok {
				ids = append(ids, s.ValueString())
			}
		}
		body["channel_ids"] = ids
	}

	return body
}

// alertRuleFromAPI builds an alertRuleModel from an API alert_rule object.
// The prior state is used to preserve values for fields the API response may omit.
func alertRuleFromAPI(ctx context.Context, obj map[string]any, prior alertRuleModel) alertRuleModel {
	m := alertRuleModel{
		ID:           stringFromAPI(obj, "id", prior.ID),
		Name:         stringFromAPI(obj, "name", prior.Name),
		ResourceType: stringFromAPI(obj, "resource_type", prior.ResourceType),
		Metric:       stringFromAPI(obj, "metric", prior.Metric),
		Operator:     stringFromAPI(obj, "operator", prior.Operator),
	}

	// resource_id: optional — may be null in the response.
	if v, ok := obj["resource_id"].(string); ok && v != "" {
		m.ResourceID = types.StringValue(v)
	} else {
		m.ResourceID = types.StringNull()
	}

	// threshold: required numeric — API returns float64 from JSON.
	switch v := obj["threshold"].(type) {
	case float64:
		m.Threshold = types.Float64Value(v)
	default:
		m.Threshold = prior.Threshold
	}

	// duration: integer, defaults to 0.
	switch v := obj["duration"].(type) {
	case float64:
		m.Duration = types.Int64Value(int64(v))
	default:
		if prior.Duration.IsNull() || prior.Duration.IsUnknown() {
			m.Duration = types.Int64Value(0)
		} else {
			m.Duration = prior.Duration
		}
	}

	// reminder_interval: integer, defaults to 0.
	switch v := obj["reminder_interval"].(type) {
	case float64:
		m.ReminderInterval = types.Int64Value(int64(v))
	default:
		if prior.ReminderInterval.IsNull() || prior.ReminderInterval.IsUnknown() {
			m.ReminderInterval = types.Int64Value(0)
		} else {
			m.ReminderInterval = prior.ReminderInterval
		}
	}

	// enabled: Laravel integer cast (1/0) or boolean.
	switch v := obj["enabled"].(type) {
	case bool:
		m.Enabled = types.BoolValue(v)
	case float64:
		m.Enabled = types.BoolValue(v != 0)
	default:
		if prior.Enabled.IsNull() || prior.Enabled.IsUnknown() {
			m.Enabled = types.BoolValue(true)
		} else {
			m.Enabled = prior.Enabled
		}
	}

	// status: server-mutable string ("ok" / "firing"). Default "ok" when absent.
	m.Status = stringFromAPI(obj, "status", prior.Status)
	if m.Status.IsNull() || m.Status.IsUnknown() || m.Status.ValueString() == "" {
		m.Status = types.StringValue("ok")
	}

	// channel_ids: extract the ids from the embedded "channels" array.
	// The SHOW/Create/Update response includes channels:[{id,name,type,...}].
	// We store only the ids as an order-independent set of strings.
	m.ChannelIDs = alertRuleChannelsToSet(ctx, obj, prior.ChannelIDs)

	return m
}

// alertRuleChannelsToSet extracts the channel UUIDs from the embedded channels
// array in the API response and returns them as a types.Set(String). Falls back
// to the prior set when the key is absent or malformed.
func alertRuleChannelsToSet(ctx context.Context, obj map[string]any, prior types.Set) types.Set {
	raw, ok := obj["channels"]
	if !ok {
		return prior
	}
	arr, ok := raw.([]any)
	if !ok {
		return prior
	}

	ids := make([]string, 0, len(arr))
	for _, v := range arr {
		ch, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := ch["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}

	set, diags := types.SetValueFrom(ctx, types.StringType, ids)
	if diags.HasError() {
		if prior.IsNull() || prior.IsUnknown() {
			empty, _ := types.SetValue(types.StringType, nil)
			return empty
		}
		return prior
	}
	return set
}
