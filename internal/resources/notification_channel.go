package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_notification_channel follows the golden ssh_key
// resource pattern (simple sync CRUD).
var (
	_ resource.Resource                = &notificationChannelResource{}
	_ resource.ResourceWithConfigure   = &notificationChannelResource{}
	_ resource.ResourceWithImportState = &notificationChannelResource{}
)

// NewNotificationChannelResource is the resource constructor registered with
// the provider.
func NewNotificationChannelResource() resource.Resource {
	return &notificationChannelResource{}
}

// notificationChannelResource manages an iaas_notification_channel.
//
// Route summary (verified against UserApi\NotificationChannelController +
// StoreRequest + UpdateRequest + NotificationChannel model +
// routes/user_api.php; all routes are gated by subuser permissions):
//
//	LIST    GET    /notification-channels              (PLURAL)
//	                → {success,channels:{current_page,data:[...],total,...}}
//	CREATE  POST   /notification-channels              (PLURAL)
//	                body {name (required,max:255),
//	                      type (required,in:slack|discord|telegram|webhook),
//	                      enabled (sometimes|boolean),
//	                      config.* (per-type, see schema)}
//	                → {success,channel:{id,name,type,enabled,...}}
//	SHOW    GET    /notification-channel/{id}          (SINGULAR)
//	                → {success,channel:{id,name,type,config,enabled,
//	                                    auto_disabled,failure_count,...}}
//	UPDATE  PATCH  /notification-channel/{id}          (SINGULAR)
//	                body same shape as CREATE (all fields updatable)
//	                → {success,channel:{id,...}}
//	DELETE  DELETE /notification-channel/{id}          (SINGULAR)
//	                → {success}
//
// Key design decisions:
//   - config is modelled as MapAttribute(String): the API accepts a name→value
//     map whose keys vary by type (webhook_url, bot_token/chat_id, url/method/
//     secret/connect_timeout/timeout/verify_ssl). Note: the headers key requires
//     an array value and is not settable via this flat string map (v1).
//     MapAttribute(String) is the cleanest representation - it round-trips cleanly
//     because the SHOW endpoint returns config decrypted (encrypted:array in DB,
//     decrypted on read, no $hidden on the model). Non-string values (bool, int)
//     from the API are coerced to strings for storage.
//   - The config map is marked Sensitive: webhook URLs typically embed auth tokens,
//     and the webhook config.secret is an HMAC key; we protect the whole map.
//   - type is updatable in place (the update request accepts any valid type +
//     matching config); it does NOT force replacement.
//   - enabled is Optional+Computed: the server defaults to 1 (true) when omitted.
//     Re-enabling a previously auto-disabled channel clears failure counters.
//   - auto_disabled is server-mutable Computed (no UseStateForUnknown): it changes
//     when the server disables a channel after repeated failures.
//   - failure_count is server-mutable Computed (no UseStateForUnknown).
//   - test action (POST /notification-channel/{id}/test) is NOT modelled - it is
//     an operational helper, not IaC state.
//   - No billing gate. Routes are gated by subuser permission monitoring.manage
//     (create/update/delete) and monitoring.view (list/show).
//   - All operations are SYNCHRONOUS (no async task/waiter).
type notificationChannelResource struct {
	client *client.Client
}

// notificationChannelModel maps the Terraform state/plan for
// iaas_notification_channel.
type notificationChannelModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Type         types.String `tfsdk:"type"`
	Config       types.Map    `tfsdk:"config"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	AutoDisabled types.Bool   `tfsdk:"auto_disabled"`
	FailureCount types.Int64  `tfsdk:"failure_count"`
}

// Metadata sets the resource type name → "<provider>_notification_channel".
func (r *notificationChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel"
}

// Schema describes the iaas_notification_channel resource.
func (r *notificationChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a notification channel - a delivery target (Slack, Discord, Telegram, " +
			"or generic webhook) that alert rules dispatch through.\n\n" +
			"The channel type determines which config keys are required:\n" +
			"  - **slack / discord**: `webhook_url` (required)\n" +
			"  - **telegram**: `bot_token` + `chat_id` (both required)\n" +
			"  - **webhook**: `url` (required), plus optional `method`, " +
			"`secret`, `connect_timeout`, `timeout`, `verify_ssl` (`\"1\"`/`\"0\"`)\n\n" +
			"All config values are stored encrypted at rest but returned decrypted by the API, " +
			"so the config map always round-trips correctly. The map is marked Sensitive to prevent " +
			"webhook URLs and tokens from appearing in plan/apply output.\n\n" +
			"The test action (POST /notification-channel/{id}/test) is an operational helper " +
			"and is not modelled as Terraform state.\n\n" +
			"Routes are gated by subuser permissions: `monitoring.view` for read operations, " +
			"`monitoring.manage` for write operations.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the notification channel, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Friendly display name for the channel. Maximum 255 characters. Updatable in place.",
			},
			"type": schema.StringAttribute{
				Required: true,
				Description: "Channel delivery type. One of: `slack`, `discord`, `telegram`, `webhook`. " +
					"Updatable in place - changing the type does not force a new resource; supply a " +
					"matching config map for the new type.",
				// type is NOT RequiresReplace: the controller accepts type changes in PATCH.
			},
			"config": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Per-type configuration map. All values are strings. Required keys " +
					"depend on the channel type:\n" +
					"  - **slack / discord**: `webhook_url` (incoming webhook URL)\n" +
					"  - **telegram**: `bot_token` (bot API token), `chat_id` (target chat ID)\n" +
					"  - **webhook**: `url` (destination URL); optional: `method` (POST or PUT), " +
					"`secret` (HMAC signing secret), `connect_timeout` (1-30 s), " +
					"`timeout` (1-60 s), `verify_ssl` (`\"1\"` to enable / `\"0\"` to disable " +
					"- the API's boolean rule only accepts 1/0 as strings)\n\n" +
					"Note: the `headers` key requires an array value and cannot be set via this " +
					"resource's flat string config map in v1; configure it via the panel or API " +
					"directly (a future typed config block may add support).\n\n" +
					"The map is marked Sensitive so webhook URLs, tokens, and secrets never " +
					"appear in plan/apply output. The API returns the config on every read " +
					"(encrypted at rest, decrypted in response), so this attribute always " +
					"round-trips correctly. Non-string values (booleans, integers) from the " +
					"API are coerced to strings for storage.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether the channel is enabled. Defaults to true when omitted. " +
					"Setting it to false disables dispatch through this channel. Re-enabling a " +
					"previously auto-disabled channel (auto_disabled = true) also clears the " +
					"failure counters server-side.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"auto_disabled": schema.BoolAttribute{
				Computed: true,
				Description: "True when the server has automatically disabled the channel after " +
					"repeated delivery failures (after 10 consecutive failures). Server-managed; " +
					"clear it by setting enabled = true, which also resets failure_count.",
				// Server-mutable: do NOT add UseStateForUnknown. The server can change
				// this field at any time, so the plan must always show the refreshed value.
			},
			"failure_count": schema.Int64Attribute{
				Computed: true,
				Description: "Number of consecutive delivery failures recorded by the server. " +
					"Server-managed. Resets to 0 when the channel is re-enabled.",
				// Server-mutable: do NOT add UseStateForUnknown.
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. Tolerates a nil
// ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *notificationChannelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the notification channel with name, type, config, and an
// optional enabled flag. The API returns the new channel with its id.
func (r *notificationChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notificationChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfgMap, diags := ncConfigToAPIMap(ctx, plan.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":   plan.Name.ValueString(),
		"type":   plan.Type.ValueString(),
		"config": cfgMap,
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	obj, err := r.client.CreateNotificationChannel(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating notification channel", err))
		return
	}

	state, d := ncStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the channel was deleted
// out of band - remove it from state so Terraform plans a recreate.
func (r *notificationChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notificationChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetNotificationChannel(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading notification channel", err))
		return
	}

	newState, d := ncStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update applies the planned changes. name, type, config, and enabled are all
// updatable in place. The PATCH response returns the full channel object.
func (r *notificationChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state notificationChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfgMap, diags := ncConfigToAPIMap(ctx, plan.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":   plan.Name.ValueString(),
		"type":   plan.Type.ValueString(),
		"config": cfgMap,
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	obj, err := r.client.UpdateNotificationChannel(ctx, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating notification channel", err))
		return
	}

	newState, d := ncStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the notification channel. The service detaches the channel
// from any alert rules before deletion.
func (r *notificationChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notificationChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteNotificationChannel(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting notification channel", err))
		return
	}
}

// ImportState lets `terraform import iaas_notification_channel.x <uuid>` adopt
// an existing channel; the next Read populates the rest of the attributes.
func (r *notificationChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ncConfigToAPIMap converts the Terraform types.Map(string) config attribute
// into a map[string]any for the API request body. Uses the shared
// parametersToAPIMap helper (same logic as db_parameter_group). An empty or
// null map yields an empty map so the API always receives a config object.
func ncConfigToAPIMap(ctx context.Context, m types.Map) (map[string]any, diag.Diagnostics) {
	return parametersToAPIMap(ctx, m)
}

// ncStateFromAPI builds a notificationChannelModel from an API channel object.
//
// The config map is decoded from map[string]any → types.Map(string) using the
// shared apiMapToParameters helper (same coercion logic as db_parameter_group).
// The prior state is used to preserve values for any field the API response omits.
func ncStateFromAPI(ctx context.Context, obj map[string]any, prior notificationChannelModel) (notificationChannelModel, diag.Diagnostics) {
	var d diag.Diagnostics

	m := notificationChannelModel{
		ID:   stringFromAPI(obj, "id", prior.ID),
		Name: stringFromAPI(obj, "name", prior.Name),
		Type: stringFromAPI(obj, "type", prior.Type),
	}

	// enabled: API returns integer 1/0 (Laravel integer cast) or boolean.
	// Coerce both to bool; fall back to prior on missing/unknown types.
	switch v := obj["enabled"].(type) {
	case bool:
		m.Enabled = types.BoolValue(v)
	case float64:
		m.Enabled = types.BoolValue(v != 0)
	default:
		m.Enabled = prior.Enabled
	}

	// auto_disabled: server-mutable boolean (Laravel boolean cast).
	switch v := obj["auto_disabled"].(type) {
	case bool:
		m.AutoDisabled = types.BoolValue(v)
	case float64:
		m.AutoDisabled = types.BoolValue(v != 0)
	default:
		// Absent on create response - default to false.
		if prior.AutoDisabled.IsNull() || prior.AutoDisabled.IsUnknown() {
			m.AutoDisabled = types.BoolValue(false)
		} else {
			m.AutoDisabled = prior.AutoDisabled
		}
	}

	// failure_count: server-mutable integer.
	switch v := obj["failure_count"].(type) {
	case float64:
		m.FailureCount = types.Int64Value(int64(v))
	default:
		// Absent on create response - default to 0.
		if prior.FailureCount.IsNull() || prior.FailureCount.IsUnknown() {
			m.FailureCount = types.Int64Value(0)
		} else {
			m.FailureCount = prior.FailureCount
		}
	}

	// config: map[string]any → types.Map(string) using the shared helper.
	cfgMap, err := apiMapToParameters(ctx, obj["config"])
	if err != nil {
		d.Append(diagFromErr("Error reading notification channel config", err))
		m.Config = prior.Config
	} else {
		m.Config = cfgMap
	}

	return m, d
}
