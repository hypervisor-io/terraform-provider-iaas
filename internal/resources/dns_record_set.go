package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions — dns_record_set is a CHILD of dns_zone (zone_id in the
// path) and a PARENT of dns_record. It copies the LB-child read-by-scan pattern
// (no individual SHOW route — record sets are embedded in the zone SHOW) and the
// vpc_subnet composite-import pattern (zone_id/record_set_id).
var (
	_ resource.Resource                = &dnsRecordSetResource{}
	_ resource.ResourceWithConfigure   = &dnsRecordSetResource{}
	_ resource.ResourceWithImportState = &dnsRecordSetResource{}
)

// NewDNSRecordSetResource is the resource constructor registered with the provider.
func NewDNSRecordSetResource() resource.Resource {
	return &dnsRecordSetResource{}
}

// dnsRecordSetResource manages an iaas_dns_record_set — a named group of DNS
// records of one type sharing a routing policy and TTL inside a zone.
//
// A record set is a genuinely distinct concept from a record: it carries the name,
// type (A/AAAA/CNAME/TXT/SRV), routing_policy (simple/weighted/multivalue/
// failover), and ttl, while its child records each carry only a value (+ weight/
// failover_role). The (name,type) pair is unique within a zone.
//
// Route summary (verified against UserApi\VpcDnsRecordSetController + VpcDnsService
// + routes/user_api.php):
//
//	CREATE  POST   /dns-zone/{zoneId}/record-sets            body {name,type,
//	                                                          routing_policy,ttl}
//	                                                          → {success,message,record_set:{id,...}}
//	UPDATE  PATCH  /dns-zone/{zoneId}/record-set/{rsId}      body {name?,type?,
//	                                                          routing_policy?,ttl?}
//	                                                          → {success,message,record_set}
//	DELETE  DELETE /dns-zone/{zoneId}/record-set/{rsId}      → {success,message}
//
// There is NO individual record-set SHOW route — Read scans the zone SHOW's
// embedded record_sets[]. All four fields are updatable in place via PATCH, so
// none is RequiresReplace except the parent zone_id (which is in the path).
type dnsRecordSetResource struct {
	client *client.Client
}

// dnsRecordSetModel maps the Terraform state/plan for iaas_dns_record_set.
type dnsRecordSetModel struct {
	ID            types.String `tfsdk:"id"`
	ZoneID        types.String `tfsdk:"zone_id"`
	Name          types.String `tfsdk:"name"`
	Type          types.String `tfsdk:"type"`
	RoutingPolicy types.String `tfsdk:"routing_policy"`
	TTL           types.Int64  `tfsdk:"ttl"`
}

// Metadata sets the resource type name → "iaas_dns_record_set".
func (r *dnsRecordSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record_set"
}

// Schema describes the iaas_dns_record_set resource.
func (r *dnsRecordSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a DNS record set inside a zone — a named group of records of one " +
			"type that share a routing policy and TTL. The (name, type) pair is unique within " +
			"a zone. The parent zone_id is part of the API path, so changing it forces a new " +
			"resource; name/type/routing_policy/ttl are all updatable in place. Add the actual " +
			"records with iaas_dns_record resources that reference this record set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the record set, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent DNS zone. Part of the API request path, so " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Record name within the zone (the label left of the zone name), e.g. " +
					"\"www\" or \"_sip._tcp\". Lowercase alphanumeric with dots, hyphens, and " +
					"underscores, max 63 chars. Updatable in place.",
			},
			"type": schema.StringAttribute{
				Required: true,
				Description: "Record type: \"A\", \"AAAA\", \"CNAME\", \"TXT\", or \"SRV\". " +
					"CNAME cannot use weighted or multivalue routing and cannot coexist with " +
					"other types for the same name. Updatable in place.",
			},
			"routing_policy": schema.StringAttribute{
				Required: true,
				Description: "How the resolver selects among the set's records: \"simple\" (one " +
					"record), \"weighted\" (weighted round-robin over A/AAAA), \"multivalue\" " +
					"(all healthy), or \"failover\" (primary/secondary). Updatable in place.",
			},
			"ttl": schema.Int64Attribute{
				Required:    true,
				Description: "Time-to-live in seconds (30–86400). Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider.
func (r *dnsRecordSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the record set under its parent zone. The create response
// carries the record set with its id.
func (r *dnsRecordSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":           plan.Name.ValueString(),
		"type":           plan.Type.ValueString(),
		"routing_policy": plan.RoutingPolicy.ValueString(),
		"ttl":            plan.TTL.ValueInt64(),
	}

	obj, err := r.client.CreateDnsRecordSet(ctx, plan.ZoneID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating DNS record set", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dnsRecordSetStateFromAPI(obj, plan))...)
}

// Read refreshes state via read-by-scan from the parent zone SHOW. A 404 (record
// set or zone gone) removes the resource from state.
func (r *dnsRecordSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetDnsRecordSet(ctx, state.ZoneID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading DNS record set", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dnsRecordSetStateFromAPI(obj, state))...)
}

// Update patches the mutable fields (name/type/routing_policy/ttl) and rehydrates
// state from the fresh record set returned by the PATCH.
func (r *dnsRecordSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan dnsRecordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields := map[string]any{
		"name":           plan.Name.ValueString(),
		"type":           plan.Type.ValueString(),
		"routing_policy": plan.RoutingPolicy.ValueString(),
		"ttl":            plan.TTL.ValueInt64(),
	}

	obj, err := r.client.UpdateDnsRecordSet(ctx, plan.ZoneID.ValueString(), plan.ID.ValueString(), fields)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating DNS record set", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dnsRecordSetStateFromAPI(obj, plan))...)
}

// Delete removes the record set (cascading its records and their health checks).
func (r *dnsRecordSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteDnsRecordSet(ctx, state.ZoneID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting DNS record set", err))
		return
	}
}

// ImportState implements COMPOSITE import: "zone_id/record_set_id" (the parent
// zone id is required to build the API path and is not derivable from the record
// set id alone).
func (r *dnsRecordSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	zoneID, rsID, ok := strings.Cut(req.ID, "/")
	if !ok || zoneID == "" || rsID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"zone_id/record_set_id\", got: %q. "+
				"DNS record sets are child resources, so both the parent zone id and the "+
				"record set id are required to import.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), zoneID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rsID)...)
}

// dnsRecordSetStateFromAPI builds the model from a record-set object, falling back
// to the prior model for any field the response omits. zone_id is never in the
// record-set body (it lives in the path), so it always falls back to prior.
func dnsRecordSetStateFromAPI(obj map[string]any, prior dnsRecordSetModel) dnsRecordSetModel {
	return dnsRecordSetModel{
		ID:            stringFromAPI(obj, "id", prior.ID),
		ZoneID:        prior.ZoneID, // never in the response body; from the path
		Name:          stringFromAPI(obj, "name", prior.Name),
		Type:          stringFromAPI(obj, "type", prior.Type),
		RoutingPolicy: stringFromAPI(obj, "routing_policy", prior.RoutingPolicy),
		TTL:           int64FromAPI(obj, "ttl", prior.TTL),
	}
}
