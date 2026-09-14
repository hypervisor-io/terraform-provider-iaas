package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

var (
	_ resource.Resource                = &microvmResource{}
	_ resource.ResourceWithConfigure   = &microvmResource{}
	_ resource.ResourceWithImportState = &microvmResource{}
)

func NewMicrovmResource() resource.Resource {
	return &microvmResource{}
}

type microvmResource struct {
	client *client.Client
}

type microvmModel struct {
	ID                  types.String `tfsdk:"id"`
	HypervisorGroupID   types.String `tfsdk:"hypervisor_group_id"`
	ImageID             types.String `tfsdk:"image_id"`
	ImageVersionID      types.String `tfsdk:"image_version_id"`
	PlanID              types.String `tfsdk:"plan_id"`
	Name                types.String `tfsdk:"name"`
	Ingress             types.Object `tfsdk:"ingress"`
	Network             types.List   `tfsdk:"network"`
	MaxLifetimeSeconds  types.Int64  `tfsdk:"max_lifetime_seconds"`
	OnTimeout           types.String `tfsdk:"on_timeout"`
	IdleTimeoutSeconds  types.Int64  `tfsdk:"idle_timeout_seconds"`
	AlwaysOn            types.Bool   `tfsdk:"always_on"`
	Env                 types.Map    `tfsdk:"env"`
	LifecycleHooks      types.String `tfsdk:"lifecycle_hooks"`
	Secure              types.Bool   `tfsdk:"secure"`
	Domains             types.Set    `tfsdk:"domains"`
	State               types.String `tfsdk:"state"`
	Fqdn                types.String `tfsdk:"fqdn"`
	E2bID               types.String `tfsdk:"e2b_id"`
	TimeoutAt           types.String `tfsdk:"timeout_at"`
	CurrentRunStatus    types.String `tfsdk:"current_run_status"`
	CurrentRunStartedAt types.String `tfsdk:"current_run_started_at"`
	Interfaces          types.List   `tfsdk:"interfaces"`
}

type microvmIngressModel struct {
	HTTP  types.Object `tfsdk:"http"`
	Shell types.Object `tfsdk:"shell"`
}

type microvmHTTPIngressModel struct {
	Enabled    types.Bool   `tfsdk:"enabled"`
	Port       types.Int64  `tfsdk:"port"`
	HealthPath types.String `tfsdk:"health_path"`
	ExtraPorts types.List   `tfsdk:"extra_ports"`
}

type microvmShellIngressModel struct {
	Enabled types.Bool `tfsdk:"enabled"`
}

type microvmNetworkModel struct {
	Kind             types.String `tfsdk:"kind"`
	SubnetID         types.String `tfsdk:"subnet_id"`
	VPCSubnetID      types.String `tfsdk:"vpc_subnet_id"`
	SecurityGroupIDs types.List   `tfsdk:"security_group_ids"`
	RateMbit         types.Int64  `tfsdk:"rate_mbit"`
}

var microvmInterfaceAttrTypes = map[string]attr.Type{
	"kind": types.StringType,
	"ipv4": types.StringType,
	"ipv6": types.StringType,
}

func (r *microvmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm"
}

func (r *microvmResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceString := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Creates and manages a MicroVM from a reusable image. Placement, image, ingress, network, and runtime policy changes replace the MicroVM; environment variables and custom domains update in place.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, Description: "UUID assigned to the MicroVM.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"hypervisor_group_id": schema.StringAttribute{Required: true, Description: "Location UUID used for placement.", PlanModifiers: replaceString},
			"image_id":            schema.StringAttribute{Required: true, Description: "Ready platform or account image UUID.", PlanModifiers: replaceString},
			"image_version_id":    schema.StringAttribute{Optional: true, Description: "Ready version UUID. The current image version is used when omitted.", PlanModifiers: replaceString},
			"plan_id":             schema.StringAttribute{Optional: true, Description: "Plan UUID. The first enabled location plan is used when omitted.", PlanModifiers: replaceString},
			"name":                schema.StringAttribute{Required: true, Description: "Account-unique lowercase name used in generated hostnames.", PlanModifiers: replaceString},
			"ingress": schema.SingleNestedAttribute{
				Optional:      true,
				Description:   "HTTP and shell ingress settings.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"http": schema.SingleNestedAttribute{
						Optional: true,
						Attributes: map[string]schema.Attribute{
							"enabled":     schema.BoolAttribute{Optional: true},
							"port":        schema.Int64Attribute{Optional: true, Description: "HTTP service port. Image detection or 8080 applies when omitted."},
							"health_path": schema.StringAttribute{Optional: true, Description: "HTTP deploy health-check path."},
							"extra_ports": schema.ListAttribute{Optional: true, ElementType: types.Int64Type, Description: "Additional HTTP ports that receive generated hostnames."},
						},
					},
					"shell": schema.SingleNestedAttribute{
						Optional: true,
						Attributes: map[string]schema.Attribute{
							"enabled": schema.BoolAttribute{Optional: true},
						},
					},
				},
			},
			"network": schema.ListNestedAttribute{
				Optional:      true,
				Description:   "Ordered isolated, public, and VPC network interfaces. An isolated interface is created when omitted.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kind":               schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("isolated", "public", "vpc")}},
					"subnet_id":          schema.StringAttribute{Optional: true, Description: "Public subnet UUID for kind = public."},
					"vpc_subnet_id":      schema.StringAttribute{Optional: true, Description: "Owned VPC subnet UUID for kind = vpc."},
					"security_group_ids": schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"rate_mbit":          schema.Int64Attribute{Optional: true, Description: "Optional interface rate limit in Mbit/s."},
				}},
			},
			"max_lifetime_seconds":   schema.Int64Attribute{Optional: true, Description: "Maximum lifetime, capped server-side at 28800 seconds.", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"on_timeout":             schema.StringAttribute{Optional: true, Description: "Timeout action: pause or kill.", Validators: []validator.String{stringvalidator.OneOf("pause", "kill")}, PlanModifiers: replaceString},
			"idle_timeout_seconds":   schema.Int64Attribute{Optional: true, Description: "Inactivity interval before pause. Null disables idle pause.", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"always_on":              schema.BoolAttribute{Optional: true, Description: "Keep the MicroVM running and restart it after failure.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"env":                    schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType, Description: "Write-only environment variables, replaceable in place."},
			"lifecycle_hooks":        schema.StringAttribute{Optional: true, Description: "Lifecycle hook overrides as a JSON object.", PlanModifiers: replaceString},
			"secure":                 schema.BoolAttribute{Optional: true, Description: "Require an access token for shell ingress.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"domains":                schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Custom hostnames reconciled through the domain endpoints."},
			"state":                  schema.StringAttribute{Computed: true, Description: "Current lifecycle state."},
			"fqdn":                   schema.StringAttribute{Computed: true, Description: "Generated HTTP hostname, when HTTP ingress is enabled."},
			"e2b_id":                 schema.StringAttribute{Computed: true, Description: "Short wire identifier assigned by the API.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"timeout_at":             schema.StringAttribute{Computed: true, Description: "Current maximum-lifetime deadline."},
			"current_run_status":     schema.StringAttribute{Computed: true, Description: "Status of the current run."},
			"current_run_started_at": schema.StringAttribute{Computed: true, Description: "Start timestamp of the current run."},
			"interfaces": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Allocated interface addresses.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{Computed: true},
					"ipv4": schema.StringAttribute{Computed: true},
					"ipv6": schema.StringAttribute{Computed: true},
				}},
			},
		},
	}
}

func (r *microvmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *microvmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := microvmCreateBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.CreateMicrovm(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MicroVM", err))
		return
	}
	state := microvmStateFromAPI(ctx, obj, nil, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.reconcileEnv(ctx, id, plan.Env, types.MapNull(types.StringType)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error setting MicroVM environment", err))
		return
	}
	if err := r.reconcileDomains(ctx, id, plan.Domains, nil); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error setting MicroVM domains", err))
		return
	}
	r.refresh(ctx, id, plan, &resp.State, &resp.Diagnostics)
}

func (r *microvmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	details, err := r.client.GetMicrovm(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM", err))
		return
	}
	obj, domains := microvmEnvelopeParts(details)
	newState := microvmStateFromAPI(ctx, obj, domains, state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *microvmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state microvmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.reconcileEnv(ctx, id, plan.Env, state.Env); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM environment", err))
		return
	}
	details, err := r.client.GetMicrovm(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM domains", err))
		return
	}
	_, domains := microvmEnvelopeParts(details)
	if err := r.reconcileDomains(ctx, id, plan.Domains, domains); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM domains", err))
		return
	}
	r.refresh(ctx, id, plan, &resp.State, &resp.Diagnostics)
}

func (r *microvmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovm(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting MicroVM", err))
	}
}

func (r *microvmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *microvmResource) refresh(ctx context.Context, id string, prior microvmModel, state *tfsdk.State, diags *diag.Diagnostics) {
	details, err := r.client.GetMicrovm(ctx, id)
	if err != nil {
		diags.Append(diagFromErr("Error reading MicroVM", err))
		return
	}
	obj, domains := microvmEnvelopeParts(details)
	model := microvmStateFromAPI(ctx, obj, domains, prior, diags)
	diags.Append(state.Set(ctx, model)...)
}

func microvmCreateBody(ctx context.Context, plan microvmModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
		"image_id":            plan.ImageID.ValueString(),
		"name":                plan.Name.ValueString(),
	}
	putOptionalString(body, "image_version_id", plan.ImageVersionID)
	putOptionalString(body, "plan_id", plan.PlanID)
	putOptionalInt64(body, "max_lifetime_seconds", plan.MaxLifetimeSeconds)
	putOptionalString(body, "on_timeout", plan.OnTimeout)
	putOptionalInt64(body, "idle_timeout_seconds", plan.IdleTimeoutSeconds)
	putOptionalBool(body, "always_on", plan.AlwaysOn)
	putOptionalBool(body, "secure", plan.Secure)

	if !plan.Ingress.IsNull() && !plan.Ingress.IsUnknown() {
		var ingress microvmIngressModel
		diags.Append(plan.Ingress.As(ctx, &ingress, basetypes.ObjectAsOptions{})...)
		if !diags.HasError() {
			body["ingress"] = microvmIngressToAPI(ctx, ingress, &diags)
		}
	}
	if !plan.Network.IsNull() && !plan.Network.IsUnknown() {
		var entries []microvmNetworkModel
		diags.Append(plan.Network.ElementsAs(ctx, &entries, false)...)
		network := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			item := map[string]any{"kind": entry.Kind.ValueString()}
			putOptionalString(item, "subnet_id", entry.SubnetID)
			putOptionalString(item, "vpc_subnet_id", entry.VPCSubnetID)
			putOptionalInt64(item, "rate_mbit", entry.RateMbit)
			if !entry.SecurityGroupIDs.IsNull() && !entry.SecurityGroupIDs.IsUnknown() {
				var ids []string
				diags.Append(entry.SecurityGroupIDs.ElementsAs(ctx, &ids, false)...)
				item["security_group_ids"] = ids
			}
			network = append(network, item)
		}
		body["network"] = network
	}
	if !plan.LifecycleHooks.IsNull() && !plan.LifecycleHooks.IsUnknown() {
		var hooks map[string]any
		if err := json.Unmarshal([]byte(plan.LifecycleHooks.ValueString()), &hooks); err != nil {
			diags.AddError("Invalid lifecycle_hooks", fmt.Sprintf("lifecycle_hooks must contain a JSON object: %s", err))
		} else {
			body["lifecycle_hooks"] = hooks
		}
	}
	return body, diags
}

func microvmIngressToAPI(ctx context.Context, ingress microvmIngressModel, diags *diag.Diagnostics) map[string]any {
	body := map[string]any{}
	if !ingress.HTTP.IsNull() && !ingress.HTTP.IsUnknown() {
		var httpIngress microvmHTTPIngressModel
		diags.Append(ingress.HTTP.As(ctx, &httpIngress, basetypes.ObjectAsOptions{})...)
		httpBody := map[string]any{}
		putOptionalBool(httpBody, "enabled", httpIngress.Enabled)
		putOptionalInt64(httpBody, "port", httpIngress.Port)
		putOptionalString(httpBody, "health_path", httpIngress.HealthPath)
		if !httpIngress.ExtraPorts.IsNull() && !httpIngress.ExtraPorts.IsUnknown() {
			var ports []int64
			diags.Append(httpIngress.ExtraPorts.ElementsAs(ctx, &ports, false)...)
			httpBody["extra_ports"] = ports
		}
		body["http"] = httpBody
	}
	if !ingress.Shell.IsNull() && !ingress.Shell.IsUnknown() {
		var shell microvmShellIngressModel
		diags.Append(ingress.Shell.As(ctx, &shell, basetypes.ObjectAsOptions{})...)
		shellBody := map[string]any{}
		putOptionalBool(shellBody, "enabled", shell.Enabled)
		body["shell"] = shellBody
	}
	return body
}

func (r *microvmResource) reconcileEnv(ctx context.Context, id string, desired, prior types.Map) error {
	if desired.Equal(prior) {
		return nil
	}
	env := map[string]string{}
	if !desired.IsNull() && !desired.IsUnknown() {
		if diags := desired.ElementsAs(ctx, &env, false); diags.HasError() {
			return fmt.Errorf("decoding env: %v", diags)
		}
	}
	_, err := r.client.SetMicrovmEnv(ctx, id, env)
	return err
}

func (r *microvmResource) reconcileDomains(ctx context.Context, id string, desired types.Set, current []map[string]any) error {
	wanted := map[string]bool{}
	if !desired.IsNull() && !desired.IsUnknown() {
		var names []string
		if diags := desired.ElementsAs(ctx, &names, false); diags.HasError() {
			return fmt.Errorf("decoding domains: %v", diags)
		}
		for _, name := range names {
			wanted[name] = true
		}
	}
	existing := map[string]string{}
	for _, domain := range current {
		hostname, _ := domain["hostname"].(string)
		domainID, _ := domain["id"].(string)
		if hostname != "" && domainID != "" {
			existing[hostname] = domainID
		}
	}
	for hostname, domainID := range existing {
		if !wanted[hostname] {
			if err := r.client.RemoveMicrovmDomain(ctx, id, domainID); err != nil {
				return err
			}
		}
	}
	for hostname := range wanted {
		if _, ok := existing[hostname]; !ok {
			if _, err := r.client.AddMicrovmDomain(ctx, id, hostname); err != nil {
				return err
			}
		}
	}
	return nil
}

// microvmEnvelopeParts splits the GetMicrovm SHOW envelope into the presented
// microvm object and its domains (always non-nil so reconcilers can
// distinguish "no domains yet" from "not fetched").
func microvmEnvelopeParts(envelope map[string]any) (map[string]any, []map[string]any) {
	obj, _ := envelope["microvm"].(map[string]any)
	domains := []map[string]any{}
	if arr, ok := envelope["domains"].([]any); ok {
		for _, entry := range arr {
			if domain, ok := entry.(map[string]any); ok {
				domains = append(domains, domain)
			}
		}
	}
	return obj, domains
}

func microvmStateFromAPI(ctx context.Context, obj map[string]any, domains []map[string]any, prior microvmModel, diags *diag.Diagnostics) microvmModel {
	state := prior
	state.ID = stringFromAPI(obj, "id", prior.ID)
	state.HypervisorGroupID = stringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID)
	state.ImageID = stringFromAPI(obj, "image_id", prior.ImageID)
	state.ImageVersionID = optionalStringFromAPI(obj, "image_version_id", prior.ImageVersionID)
	state.PlanID = optionalStringFromAPI(obj, "plan_id", prior.PlanID)
	state.Name = stringFromAPI(obj, "name", prior.Name)
	state.State = stringFromAPI(obj, "state", prior.State)
	state.Fqdn = optionalStringFromAPI(obj, "fqdn", prior.Fqdn)
	state.E2bID = stringFromAPI(obj, "e2b_sandbox_id", prior.E2bID)
	state.TimeoutAt = optionalStringFromAPI(obj, "timeout_at", prior.TimeoutAt)
	if run, ok := obj["current_run"].(map[string]any); ok {
		state.CurrentRunStatus = stringFromAPI(run, "status", prior.CurrentRunStatus)
		state.CurrentRunStartedAt = optionalStringFromAPI(run, "started_at", prior.CurrentRunStartedAt)
	}
	if domains != nil {
		names := make([]string, 0, len(domains))
		for _, domain := range domains {
			if hostname, ok := domain["hostname"].(string); ok {
				names = append(names, hostname)
			}
		}
		set, domainDiags := types.SetValueFrom(ctx, types.StringType, names)
		diags.Append(domainDiags...)
		state.Domains = set
	}
	interfaces, interfaceDiags := microvmInterfacesFromAPI(obj["interfaces"])
	diags.Append(interfaceDiags...)
	state.Interfaces = interfaces
	return state
}

func microvmInterfacesFromAPI(raw any) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: microvmInterfaceAttrTypes}
	items, ok := raw.([]any)
	if !ok {
		return types.ListNull(objectType), nil
	}
	values := make([]attr.Value, 0, len(items))
	var diags diag.Diagnostics
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		value, itemDiags := types.ObjectValue(microvmInterfaceAttrTypes, map[string]attr.Value{
			"kind": nullableStringAttr(item["kind"]),
			"ipv4": nullableStringAttr(item["ipv4"]),
			"ipv6": nullableStringAttr(item["ipv6"]),
		})
		diags.Append(itemDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}

func nullableStringAttr(raw any) types.String {
	if raw == nil {
		return types.StringNull()
	}
	if value, ok := raw.(string); ok {
		return types.StringValue(value)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}

func putOptionalInt64(body map[string]any, key string, value types.Int64) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueInt64()
	}
}

func putOptionalBool(body map[string]any, key string, value types.Bool) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueBool()
	}
}
