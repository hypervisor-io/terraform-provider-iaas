package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions - dns_record is a CHILD of dns_record_set (zone_id +
// record_set_id in the path). It copies the read-by-scan pattern (records are
// embedded in the zone SHOW under record_sets[].records[]) and the 3-part
// composite-import pattern (zone_id/record_set_id/record_id, like lb_target).
// It additionally owns a 1:1 health_check as an inline nested single block,
// reconciled via the record's store/delete health-check endpoints on diff.
var (
	_ resource.Resource                = &dnsRecordResource{}
	_ resource.ResourceWithConfigure   = &dnsRecordResource{}
	_ resource.ResourceWithImportState = &dnsRecordResource{}
)

// NewDNSRecordResource is the resource constructor registered with the provider.
func NewDNSRecordResource() resource.Resource {
	return &dnsRecordResource{}
}

// dnsRecordResource manages an iaas_dns_record - a single value within a record
// set (e.g. one IP for an A record set), optionally with a health check.
//
// Route summary (verified against UserApi\VpcDnsRecordController + VpcDnsService +
// routes/user_api.php):
//
//	CREATE  POST   .../record-set/{rsId}/records          body {value (req), weight?,
//	                                                      failover_role?, enabled?}
//	                                                      → {success,message,record:{id,...}}
//	UPDATE  PATCH  .../record-set/{rsId}/record/{recId}   body {value?, weight?,
//	                                                      failover_role?, enabled?}
//	                                                      → {success,message,record}
//	DELETE  DELETE .../record-set/{rsId}/record/{recId}   → {success,message}
//	HC STORE POST  .../record/{recId}/health-check        body {type,port?,path?,...}
//	HC DEL  DELETE .../record/{recId}/health-check
//
// value/weight/failover_role/enabled are all updatable in place via PATCH (none
// is RequiresReplace except the parent path ids). The health check is an optional
// inline nested single block reconciled separately via its store/delete endpoints.
type dnsRecordResource struct {
	client *client.Client
}

// dnsRecordModel maps the Terraform state/plan for iaas_dns_record.
type dnsRecordModel struct {
	ID           types.String `tfsdk:"id"`
	ZoneID       types.String `tfsdk:"zone_id"`
	RecordSetID  types.String `tfsdk:"record_set_id"`
	Value        types.String `tfsdk:"value"`
	Weight       types.Int64  `tfsdk:"weight"`
	FailoverRole types.String `tfsdk:"failover_role"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	IsHealthy    types.Bool   `tfsdk:"is_healthy"`
	HealthCheck  types.Object `tfsdk:"health_check"`
}

// dnsHealthCheckModel maps the nested health_check block.
type dnsHealthCheckModel struct {
	Type               types.String `tfsdk:"type"`
	Port               types.Int64  `tfsdk:"port"`
	Path               types.String `tfsdk:"path"`
	ExpectedStatus     types.Int64  `tfsdk:"expected_status"`
	Interval           types.Int64  `tfsdk:"interval"`
	Timeout            types.Int64  `tfsdk:"timeout"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
}

// dnsHealthCheckAttrTypes is the attribute-type map for the nested health_check
// object - the single source of truth reused everywhere a health_check object is
// (re)built so the schema and runtime values never drift.
var dnsHealthCheckAttrTypes = map[string]attr.Type{
	"type":                types.StringType,
	"port":                types.Int64Type,
	"path":                types.StringType,
	"expected_status":     types.Int64Type,
	"interval":            types.Int64Type,
	"timeout":             types.Int64Type,
	"unhealthy_threshold": types.Int64Type,
	"healthy_threshold":   types.Int64Type,
}

// Metadata sets the resource type name → "iaas_dns_record".
func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

// Schema describes the iaas_dns_record resource.
func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single DNS record (one value) within a record set. The value " +
			"format is validated server-side against the record set's type (A→IPv4, " +
			"AAAA→IPv6, CNAME→hostname, SRV→\"priority weight port target\"). An optional " +
			"health_check block attaches an active health check to this record; an unhealthy " +
			"record is withheld from resolution (fail-open if all are unhealthy).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the record, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the DNS zone that owns the parent record set. Part of the " +
					"API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"record_set_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent record set. Part of the API request path, so " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				Required: true,
				Description: "The record value. Its format must match the record set type: an " +
					"IPv4 address for A, an IPv6 address for AAAA, a hostname for CNAME, the text " +
					"for TXT, or \"priority weight port target\" for SRV. Updatable in place.",
			},
			"weight": schema.Int64Attribute{
				Optional: true,
				Description: "Relative weight (1-255) for weighted routing. Required by the API " +
					"when the record set uses the \"weighted\" policy; ignored otherwise. " +
					"Updatable in place.",
			},
			"failover_role": schema.StringAttribute{
				Optional: true,
				Description: "Role for failover routing: \"primary\" or \"secondary\". Required by " +
					"the API when the record set uses the \"failover\" policy; ignored otherwise. " +
					"Updatable in place.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether this record participates in resolution. Defaults to true. " +
					"Updatable in place.",
			},
			"is_healthy": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the record is currently considered healthy by its health " +
					"check. Server-managed (true when no health check is attached).",
				// No UseStateForUnknown: is_healthy is server-mutable (flipped by health
				// reports), so masking it would hide real drift.
			},
			"health_check": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Optional active health check for this record. When present, the " +
					"record is withheld from resolution while unhealthy (fail-open if all " +
					"records in the set are unhealthy). Remove the block to detach the check.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Required:    true,
						Description: "Probe type: \"http\", \"https\", \"tcp\", or \"icmp\".",
					},
					"port": schema.Int64Attribute{
						Optional:    true,
						Description: "Probe port (1-65535). Defaults server-side by type.",
					},
					"path": schema.StringAttribute{
						Optional:    true,
						Description: "HTTP(S) probe path, e.g. \"/health\". For http/https only.",
					},
					"expected_status": schema.Int64Attribute{
						Optional:    true,
						Description: "Expected HTTP status code (100-599) for http/https probes.",
					},
					"interval": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Seconds between probes (10-300). Optional; null is stored when " +
							"omitted - the check agent applies its own default.",
					},
					"timeout": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Probe timeout in seconds (2-60). Optional; null is stored when " +
							"omitted - the check agent applies its own default.",
					},
					"unhealthy_threshold": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Consecutive failures before the record is marked unhealthy " +
							"(1-10). Optional; null is stored when omitted - the check agent " +
							"applies its own default.",
					},
					"healthy_threshold": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Consecutive successes before the record is marked healthy " +
							"again (1-10). Optional; null is stored when omitted - the check " +
							"agent applies its own default.",
					},
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider.
func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the record under its parent record set, then attaches the
// health check if one was configured, then reads back so state reflects the
// server defaults (enabled/is_healthy) and the persisted health-check values.
func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"value": plan.Value.ValueString()}
	if !plan.Weight.IsNull() && !plan.Weight.IsUnknown() {
		body["weight"] = plan.Weight.ValueInt64()
	}
	if !plan.FailoverRole.IsNull() && !plan.FailoverRole.IsUnknown() {
		body["failover_role"] = plan.FailoverRole.ValueString()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	zoneID := plan.ZoneID.ValueString()
	rsID := plan.RecordSetID.ValueString()

	obj, err := r.client.CreateDnsRecord(ctx, zoneID, rsID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating DNS record", err))
		return
	}
	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating DNS record", "the create response did not contain an id")
		return
	}

	// Attach the health check if configured.
	if !plan.HealthCheck.IsNull() && !plan.HealthCheck.IsUnknown() {
		if err := r.storeHealthCheck(ctx, zoneID, rsID, id, plan.HealthCheck); err != nil {
			// Persist a minimal destroyable state (record exists) before bailing.
			_ = resp.State.Set(ctx, dnsRecordStateFromAPI(obj, plan))
			resp.Diagnostics.Append(diagFromErr("Error attaching DNS health check", err))
			return
		}
	}

	// Read back so state reflects server defaults + the persisted health check.
	state, notFound, diags := r.readState(ctx, zoneID, rsID, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating DNS record",
			"the DNS record disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state via read-by-scan from the parent zone SHOW. A 404 (record,
// record set, or zone gone) removes the resource from state.
func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, diags := r.readState(ctx,
		state.ZoneID.ValueString(), state.RecordSetID.ValueString(), state.ID.ValueString(), state)
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update patches the mutable record fields, reconciles the health check (store
// when added/changed, delete when removed), then reads back.
func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := plan.ZoneID.ValueString()
	rsID := plan.RecordSetID.ValueString()
	id := plan.ID.ValueString()

	// Patch the record scalars when any changed. value is always sent; weight/
	// failover_role are sent (null when cleared) and enabled when known.
	if !plan.Value.Equal(state.Value) || !plan.Weight.Equal(state.Weight) ||
		!plan.FailoverRole.Equal(state.FailoverRole) || !plan.Enabled.Equal(state.Enabled) {
		fields := map[string]any{"value": plan.Value.ValueString()}
		if plan.Weight.IsNull() {
			fields["weight"] = nil
		} else if !plan.Weight.IsUnknown() {
			fields["weight"] = plan.Weight.ValueInt64()
		}
		if plan.FailoverRole.IsNull() {
			fields["failover_role"] = nil
		} else if !plan.FailoverRole.IsUnknown() {
			fields["failover_role"] = plan.FailoverRole.ValueString()
		}
		if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
			fields["enabled"] = plan.Enabled.ValueBool()
		}
		if _, err := r.client.UpdateDnsRecord(ctx, zoneID, rsID, id, fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating DNS record", err))
			return
		}
	}

	// Reconcile the health check.
	planHasHC := !plan.HealthCheck.IsNull() && !plan.HealthCheck.IsUnknown()
	stateHasHC := !state.HealthCheck.IsNull() && !state.HealthCheck.IsUnknown()
	switch {
	case planHasHC && (!stateHasHC || !plan.HealthCheck.Equal(state.HealthCheck)):
		// storeHealthCheck is create-OR-update (1:1), so it covers add and change.
		if err := r.storeHealthCheck(ctx, zoneID, rsID, id, plan.HealthCheck); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating DNS health check", err))
			return
		}
	case !planHasHC && stateHasHC:
		if err := r.client.DeleteDnsHealthCheck(ctx, zoneID, rsID, id); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error detaching DNS health check", err))
			return
		}
	}

	newState, notFound, diags := r.readState(ctx, zoneID, rsID, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating DNS record", "the DNS record disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the record (its health check cascades server-side).
func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteDnsRecord(ctx,
		state.ZoneID.ValueString(), state.RecordSetID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting DNS record", err))
		return
	}
}

// ImportState implements 3-part COMPOSITE import:
// "zone_id/record_set_id/record_id" (both parent ids are required to build the
// API path and scan the zone SHOW). The health_check block is rebuilt by the
// subsequent Read from the record's embedded health_check.
func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"zone_id/record_set_id/record_id\", got: %q. "+
				"DNS records are grandchild resources, so the zone id, record set id, and "+
				"record id are all required to import.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("record_set_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

// storeHealthCheck decodes the nested health_check object and POSTs it (create-or-
// update). Optional unset fields are omitted so the server applies its defaults.
func (r *dnsRecordResource) storeHealthCheck(ctx context.Context, zoneID, rsID, recID string, hc types.Object) error {
	var m dnsHealthCheckModel
	if d := hc.As(ctx, &m, basetypes.ObjectAsOptions{}); d.HasError() {
		return fmt.Errorf("decoding health_check block: %v", d.Errors())
	}
	body := map[string]any{"type": m.Type.ValueString()}
	putInt := func(key string, v types.Int64) {
		if !v.IsNull() && !v.IsUnknown() {
			body[key] = v.ValueInt64()
		}
	}
	putInt("port", m.Port)
	if !m.Path.IsNull() && !m.Path.IsUnknown() {
		body["path"] = m.Path.ValueString()
	}
	putInt("expected_status", m.ExpectedStatus)
	putInt("interval", m.Interval)
	putInt("timeout", m.Timeout)
	putInt("unhealthy_threshold", m.UnhealthyThreshold)
	putInt("healthy_threshold", m.HealthyThreshold)

	_, err := r.client.StoreDnsHealthCheck(ctx, zoneID, rsID, recID, body)
	return err
}

// readState scans the zone SHOW for the record and builds the model. The bool
// return is true when the record was not found (404).
func (r *dnsRecordResource) readState(ctx context.Context, zoneID, rsID, recID string, prior dnsRecordModel) (dnsRecordModel, bool, diag.Diagnostics) {
	obj, err := r.client.GetDnsRecord(ctx, zoneID, rsID, recID)
	if err != nil {
		if client.IsNotFound(err) {
			return dnsRecordModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading DNS record", err))
		return dnsRecordModel{}, false, diags
	}
	m, diags := dnsRecordStateFromAPIFull(obj, prior)
	return m, false, diags
}

// dnsRecordStateFromAPI builds the model from a record object WITHOUT touching the
// health_check (used on the create-response path before the read-back, where the
// create response carries no health_check). It preserves the prior health_check.
func dnsRecordStateFromAPI(obj map[string]any, prior dnsRecordModel) dnsRecordModel {
	return dnsRecordModel{
		ID:           stringFromAPI(obj, "id", prior.ID),
		ZoneID:       prior.ZoneID,
		RecordSetID:  prior.RecordSetID,
		Value:        stringFromAPI(obj, "value", prior.Value),
		Weight:       optionalInt64FromAPI(obj, "weight"),
		FailoverRole: optionalStringFromAPI(obj, "failover_role", prior.FailoverRole),
		Enabled:      boolFromIntAPI(obj, "enabled", prior.Enabled),
		IsHealthy:    boolFromIntAPI(obj, "is_healthy", prior.IsHealthy),
		HealthCheck:  prior.HealthCheck,
	}
}

// dnsRecordStateFromAPIFull builds the model from a record object including the
// embedded health_check (used on Read/read-back). zone_id/record_set_id are never
// in the record body (they live in the path), so they fall back to prior.
func dnsRecordStateFromAPIFull(obj map[string]any, prior dnsRecordModel) (dnsRecordModel, diag.Diagnostics) {
	m := dnsRecordStateFromAPI(obj, prior)
	hc, diags := healthCheckObjectFromAPI(obj["health_check"], prior.HealthCheck)
	m.HealthCheck = hc
	return m, diags
}

// healthCheckObjectFromAPI converts the embedded "health_check" JSON object (or
// null) into a types.Object. A null/absent health check stays null so an
// unmanaged-health-check config round-trips without drift.
func healthCheckObjectFromAPI(raw any, prior types.Object) (types.Object, diag.Diagnostics) {
	hc, ok := raw.(map[string]any)
	if !ok || hc == nil {
		return types.ObjectNull(dnsHealthCheckAttrTypes), nil
	}
	obj, d := types.ObjectValue(dnsHealthCheckAttrTypes, map[string]attr.Value{
		"type":                stringFromAPI(hc, "type", types.StringNull()),
		"port":                optionalInt64FromAPI(hc, "port"),
		"path":                optionalStringFromAPI(hc, "path", types.StringNull()),
		"expected_status":     optionalInt64FromAPI(hc, "expected_status"),
		"interval":            optionalInt64FromAPI(hc, "interval"),
		"timeout":             optionalInt64FromAPI(hc, "timeout"),
		"unhealthy_threshold": optionalInt64FromAPI(hc, "unhealthy_threshold"),
		"healthy_threshold":   optionalInt64FromAPI(hc, "healthy_threshold"),
	})
	if d.HasError() {
		return types.ObjectNull(dnsHealthCheckAttrTypes), d
	}
	return obj, nil
}
