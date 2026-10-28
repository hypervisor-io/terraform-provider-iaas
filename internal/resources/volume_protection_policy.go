package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

var (
	_ resource.Resource                = &volumeProtectionPolicyResource{}
	_ resource.ResourceWithConfigure   = &volumeProtectionPolicyResource{}
	_ resource.ResourceWithImportState = &volumeProtectionPolicyResource{}
)

func NewVolumeProtectionPolicyResource() resource.Resource { return &volumeProtectionPolicyResource{} }

type volumeProtectionPolicyResource struct{ client *client.Client }
type volumeProtectionPolicyModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Frequency         types.String `tfsdk:"frequency"`
	ScheduleTime      types.String `tfsdk:"schedule_time"`
	ScheduleDay       types.Int64  `tfsdk:"schedule_day"`
	Timezone          types.String `tfsdk:"timezone"`
	BackupEnabled     types.Bool   `tfsdk:"backup_enabled"`
	SnapshotEnabled   types.Bool   `tfsdk:"snapshot_enabled"`
	RetentionMode     types.String `tfsdk:"retention_mode"`
	RetentionCount    types.Int64  `tfsdk:"retention_count"`
	KeepLast          types.Int64  `tfsdk:"keep_last"`
	KeepWithinDays    types.Int64  `tfsdk:"keep_within_days"`
	KeepDaily         types.Int64  `tfsdk:"keep_daily"`
	KeepWeekly        types.Int64  `tfsdk:"keep_weekly"`
	KeepMonthly       types.Int64  `tfsdk:"keep_monthly"`
	KeepYearly        types.Int64  `tfsdk:"keep_yearly"`
	Status            types.String `tfsdk:"status"`
	EffectiveTimezone types.String `tfsdk:"effective_timezone"`
}

func (r *volumeProtectionPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_protection_policy"
}
func (r *volumeProtectionPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Schedules standalone volume backups and snapshots. Wall-clock schedules follow DST in the named timezone or the account owner's effective timezone. Manage membership with iaas_volume_protection_policy_attachment. Policies pause after consecutive failures; resume with the API or MCP reset_failures action. Deleting a policy detaches volumes and keeps its existing artifacts.",
		Attributes: map[string]schema.Attribute{
			"id":                 schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Policy UUID. Import using this UUID."},
			"name":               schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}, Description: "Policy name, up to 255 characters."},
			"frequency":          schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("daily", "weekly")}, Description: "daily or weekly."},
			"schedule_time":      schema.StringAttribute{Required: true, Description: "HH:MM wall-clock time in the effective timezone. Never converted to UTC by the provider."},
			"schedule_day":       schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 6)}, Description: "Weekly day: 0 Sunday through 6 Saturday. Required for weekly; omit for daily."},
			"timezone":           schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}, Description: "Named IANA timezone. Omit or null to follow the account owner's timezone. Removing a configured zone clears it on the API."},
			"backup_enabled":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Enable off-host backups. At least backups or snapshots must be enabled."},
			"snapshot_enabled":   schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Enable volume snapshots. Proxmox per-volume protection is unsupported."},
			"retention_mode":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("count"), Validators: []validator.String{stringvalidator.OneOf("count", "gfs")}, Description: "count keeps retention_count artifacts per kind per volume; gfs applies keep_* rules only to this policy's artifacts."},
			"retention_count":    schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{int64validator.Between(1, 365)}, Description: "1..365, required for count retention on create. Null on update keeps the stored count, as required by the API."},
			"status":             schema.StringAttribute{Computed: true, Description: "Server status: active or error (paused)."},
			"effective_timezone": schema.StringAttribute{Computed: true, Description: "Resolved schedule timezone, including the account owner fallback."},
			"keep_last":          schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 365)}, Description: "GFS newest artifacts, 0..365. Zero or null disables this rule. At least one GFS rule must be positive."},
			"keep_within_days":   schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 3650)}, Description: "GFS days of all artifacts, 0..3650. Zero or null disables this rule. At least one GFS rule must be positive."},
			"keep_daily":         schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 365)}, Description: "GFS daily buckets, 0..365. Zero or null disables this rule. At least one GFS rule must be positive."},
			"keep_weekly":        schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 520)}, Description: "GFS ISO week buckets, 0..520. Zero or null disables this rule. At least one GFS rule must be positive."},
			"keep_monthly":       schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 120)}, Description: "GFS calendar month buckets, 0..120. Zero or null disables this rule. At least one GFS rule must be positive."},
			"keep_yearly":        schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(0, 30)}, Description: "GFS calendar year buckets, 0..30. Zero or null disables this rule. At least one GFS rule must be positive."},
		}}
}
func (r *volumeProtectionPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}
func (r *volumeProtectionPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeProtectionPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.CreateVolumeProtectionPolicy(ctx, volumeProtectionBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating volume protection policy", err))
		return
	}
	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Invalid policy response", "Create response omitted the policy UUID; inspect policies before retrying.")
		return
	}
	// The mutation response contains all scalar fields. No follow-up GET can
	// discard an already-created policy if a later network read fails.
	plan.ID = types.StringValue(id)
	plan = volumeProtectionState(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *volumeProtectionPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeProtectionPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetVolumeProtectionPolicy(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading volume protection policy", err))
		return
	}
	state = volumeProtectionState(obj, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
func (r *volumeProtectionPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeProtectionPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.UpdateVolumeProtectionPolicy(ctx, state.ID.ValueString(), volumeProtectionBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating volume protection policy", err))
		return
	}
	plan.ID = state.ID
	plan = volumeProtectionState(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *volumeProtectionPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeProtectionPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteVolumeProtectionPolicy(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("Error deleting volume protection policy", err))
	}
}
func (r *volumeProtectionPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
func volumeProtectionBody(p volumeProtectionPolicyModel) map[string]any {
	body := map[string]any{}
	if !p.Name.IsUnknown() {
		if p.Name.IsNull() {
			body["name"] = nil
		} else {
			body["name"] = p.Name.ValueString()
		}
	}
	if !p.Frequency.IsUnknown() {
		if p.Frequency.IsNull() {
			body["frequency"] = nil
		} else {
			body["frequency"] = p.Frequency.ValueString()
		}
	}
	if !p.ScheduleTime.IsUnknown() {
		if p.ScheduleTime.IsNull() {
			body["schedule_time"] = nil
		} else {
			body["schedule_time"] = p.ScheduleTime.ValueString()
		}
	}
	if !p.ScheduleDay.IsUnknown() {
		if p.ScheduleDay.IsNull() {
			body["schedule_day"] = nil
		} else {
			body["schedule_day"] = p.ScheduleDay.ValueInt64()
		}
	}
	if !p.Timezone.IsUnknown() {
		if p.Timezone.IsNull() {
			body["timezone"] = nil
		} else {
			body["timezone"] = p.Timezone.ValueString()
		}
	}
	if !p.BackupEnabled.IsUnknown() {
		if p.BackupEnabled.IsNull() {
			body["backup_enabled"] = nil
		} else {
			body["backup_enabled"] = p.BackupEnabled.ValueBool()
		}
	}
	if !p.SnapshotEnabled.IsUnknown() {
		if p.SnapshotEnabled.IsNull() {
			body["snapshot_enabled"] = nil
		} else {
			body["snapshot_enabled"] = p.SnapshotEnabled.ValueBool()
		}
	}
	if !p.RetentionMode.IsUnknown() {
		if p.RetentionMode.IsNull() {
			body["retention_mode"] = nil
		} else {
			body["retention_mode"] = p.RetentionMode.ValueString()
		}
	}
	if !p.RetentionCount.IsUnknown() {
		if p.RetentionCount.IsNull() {
			body["retention_count"] = nil
		} else {
			body["retention_count"] = p.RetentionCount.ValueInt64()
		}
	}
	if !p.KeepLast.IsUnknown() {
		if p.KeepLast.IsNull() {
			body["keep_last"] = nil
		} else {
			body["keep_last"] = p.KeepLast.ValueInt64()
		}
	}
	if !p.KeepWithinDays.IsUnknown() {
		if p.KeepWithinDays.IsNull() {
			body["keep_within_days"] = nil
		} else {
			body["keep_within_days"] = p.KeepWithinDays.ValueInt64()
		}
	}
	if !p.KeepDaily.IsUnknown() {
		if p.KeepDaily.IsNull() {
			body["keep_daily"] = nil
		} else {
			body["keep_daily"] = p.KeepDaily.ValueInt64()
		}
	}
	if !p.KeepWeekly.IsUnknown() {
		if p.KeepWeekly.IsNull() {
			body["keep_weekly"] = nil
		} else {
			body["keep_weekly"] = p.KeepWeekly.ValueInt64()
		}
	}
	if !p.KeepMonthly.IsUnknown() {
		if p.KeepMonthly.IsNull() {
			body["keep_monthly"] = nil
		} else {
			body["keep_monthly"] = p.KeepMonthly.ValueInt64()
		}
	}
	if !p.KeepYearly.IsUnknown() {
		if p.KeepYearly.IsNull() {
			body["keep_yearly"] = nil
		} else {
			body["keep_yearly"] = p.KeepYearly.ValueInt64()
		}
	}
	return body
}
func volumeProtectionState(obj map[string]any, p volumeProtectionPolicyModel) volumeProtectionPolicyModel {
	if v, ok := obj["name"]; ok {
		if v == nil {
			p.Name = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.Name = types.StringValue(value)
		}
	}
	if v, ok := obj["frequency"]; ok {
		if v == nil {
			p.Frequency = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.Frequency = types.StringValue(value)
		}
	}
	if v, ok := obj["schedule_time"]; ok {
		if v == nil {
			p.ScheduleTime = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.ScheduleTime = types.StringValue(value)
		}
	}
	if v, ok := obj["schedule_day"]; ok {
		if v == nil {
			p.ScheduleDay = types.Int64Null()
		} else {
			p.ScheduleDay = int64FromAPI(obj, "schedule_day", p.ScheduleDay)
		}
	}
	if v, ok := obj["timezone"]; ok {
		if v == nil {
			p.Timezone = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.Timezone = types.StringValue(value)
		}
	}
	p.BackupEnabled = boolFromIntAPI(obj, "backup_enabled", p.BackupEnabled)
	p.SnapshotEnabled = boolFromIntAPI(obj, "snapshot_enabled", p.SnapshotEnabled)
	if v, ok := obj["retention_mode"]; ok {
		if v == nil {
			p.RetentionMode = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.RetentionMode = types.StringValue(value)
		}
	}
	if v, ok := obj["retention_count"]; ok {
		if v == nil {
			p.RetentionCount = types.Int64Null()
		} else {
			p.RetentionCount = int64FromAPI(obj, "retention_count", p.RetentionCount)
		}
	}
	if v, ok := obj["keep_last"]; ok {
		if v == nil && !(p.KeepLast.Equal(types.Int64Value(0))) {
			p.KeepLast = types.Int64Null()
		} else {
			p.KeepLast = int64FromAPI(obj, "keep_last", p.KeepLast)
		}
	}
	if v, ok := obj["keep_within_days"]; ok {
		if v == nil && !(p.KeepWithinDays.Equal(types.Int64Value(0))) {
			p.KeepWithinDays = types.Int64Null()
		} else {
			p.KeepWithinDays = int64FromAPI(obj, "keep_within_days", p.KeepWithinDays)
		}
	}
	if v, ok := obj["keep_daily"]; ok {
		if v == nil && !(p.KeepDaily.Equal(types.Int64Value(0))) {
			p.KeepDaily = types.Int64Null()
		} else {
			p.KeepDaily = int64FromAPI(obj, "keep_daily", p.KeepDaily)
		}
	}
	if v, ok := obj["keep_weekly"]; ok {
		if v == nil && !(p.KeepWeekly.Equal(types.Int64Value(0))) {
			p.KeepWeekly = types.Int64Null()
		} else {
			p.KeepWeekly = int64FromAPI(obj, "keep_weekly", p.KeepWeekly)
		}
	}
	if v, ok := obj["keep_monthly"]; ok {
		if v == nil && !(p.KeepMonthly.Equal(types.Int64Value(0))) {
			p.KeepMonthly = types.Int64Null()
		} else {
			p.KeepMonthly = int64FromAPI(obj, "keep_monthly", p.KeepMonthly)
		}
	}
	if v, ok := obj["keep_yearly"]; ok {
		if v == nil && !(p.KeepYearly.Equal(types.Int64Value(0))) {
			p.KeepYearly = types.Int64Null()
		} else {
			p.KeepYearly = int64FromAPI(obj, "keep_yearly", p.KeepYearly)
		}
	}
	if v, ok := obj["status"]; ok {
		if v == nil {
			p.Status = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.Status = types.StringValue(value)
		}
	}
	if v, ok := obj["effective_timezone"]; ok {
		if v == nil {
			p.EffectiveTimezone = types.StringNull()
		} else if value, ok := v.(string); ok {
			p.EffectiveTimezone = types.StringValue(value)
		}
	}
	return p
}

var _ resource.ResourceWithValidateConfig = &volumeProtectionPolicyResource{}

func (r *volumeProtectionPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config volumeProtectionPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Frequency.ValueString() == "daily" && !config.ScheduleDay.IsNull() && !config.ScheduleDay.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("schedule_day"), "Daily policies have no schedule day", "Omit schedule_day for a daily policy; the API always clears it.")
	}
	if config.Frequency.ValueString() == "weekly" && config.ScheduleDay.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("schedule_day"), "Weekly policy requires a day", "Set schedule_day from 0 (Sunday) through 6 (Saturday).")
	}
}
