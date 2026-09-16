package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// microvmSettingsSingletonID is the fixed state id for this account-scoped
// singleton (there is exactly one default network per account, keyed off the
// bearer token - the API has no id to import against). Any future
// account-level settings-style resource this provider grows should follow
// the same shape rather than inventing a new one.
const microvmSettingsSingletonID = "default"

var (
	_ resource.Resource                = &microvmSettingsResource{}
	_ resource.ResourceWithConfigure   = &microvmSettingsResource{}
	_ resource.ResourceWithImportState = &microvmSettingsResource{}
)

func NewMicrovmSettingsResource() resource.Resource {
	return &microvmSettingsResource{}
}

type microvmSettingsResource struct {
	client *client.Client
}

type microvmSettingsModel struct {
	ID             types.String `tfsdk:"id"`
	DefaultNetwork types.Object `tfsdk:"default_network"`
}

type microvmSettingsDefaultNetworkModel struct {
	Kind        types.String `tfsdk:"kind"`
	SubnetID    types.String `tfsdk:"subnet_id"`
	VPCSubnetID types.String `tfsdk:"vpc_subnet_id"`
}

func microvmSettingsDefaultNetworkTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"kind":          types.StringType,
		"subnet_id":     types.StringType,
		"vpc_subnet_id": types.StringType,
	}
}

func (r *microvmSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_settings"
}

func (r *microvmSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the account's default microVM network (contract C4/C8.6), used by every iaas_microvm/E2B create that omits network. This is a per-account singleton: creating it sets the default, and there is only ever one per account/token. Deleting it clears the default (PUT /microvm/settings with default_network = null).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Always \"default\" - this resource is a per-account singleton.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"default_network": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "The default network entry, or omit/null to clear it.",
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						Required:    true,
						Description: "public or vpc.",
						Validators:  []validator.String{stringvalidator.OneOf("public", "vpc")},
					},
					"subnet_id": schema.StringAttribute{
						Optional:    true,
						Description: "kind = public only. Optional - the platform auto-assigns a public subnet at create time when omitted (C8.1/C8.6); this stays accepted for an explicit choice.",
					},
					"vpc_subnet_id": schema.StringAttribute{
						Optional:    true,
						Description: "kind = vpc only. Required for a vpc default: a VPC subnet owned by the account.",
					},
				},
			},
		},
	}
}

func (r *microvmSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *microvmSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.put(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *microvmSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	envelope, err := r.client.GetMicrovmSettings(ctx)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading microVM settings", err))
		return
	}
	newState, diags := microvmSettingsStateFromAPI(envelope)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *microvmSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.put(ctx, plan, &resp.State, &resp.Diagnostics)
}

// Delete clears the account's default network (PUT default_network: null,
// C4/C8.6) rather than issuing a DELETE - the API models "no default" as a
// null column, not a missing row. There is no dedicated clear endpoint.
func (r *microvmSettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if _, err := r.client.UpdateMicrovmSettings(ctx, nil); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error clearing microVM settings", err))
	}
}

func (r *microvmSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// put sends the plan's default_network to PUT /microvm/settings and writes
// the echoed value back into state - the shared body of Create and Update
// (services.md twin rule: one body, not two drifting copies).
func (r *microvmSettingsResource) put(ctx context.Context, plan microvmSettingsModel, state *tfsdk.State, diags *diag.Diagnostics) {
	body, bodyDiags := microvmSettingsDefaultNetworkToAPI(ctx, plan.DefaultNetwork)
	diags.Append(bodyDiags...)
	if diags.HasError() {
		return
	}
	envelope, err := r.client.UpdateMicrovmSettings(ctx, body)
	if err != nil {
		diags.Append(diagFromErr("Error setting microVM settings", err))
		return
	}
	newState, stateDiags := microvmSettingsStateFromAPI(envelope)
	diags.Append(stateDiags...)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, newState)...)
}

// microvmSettingsDefaultNetworkToAPI converts the plan's default_network
// object (or null/unknown, to clear) into the PUT body's default_network
// value.
func microvmSettingsDefaultNetworkToAPI(ctx context.Context, obj types.Object) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, diags
	}
	var model microvmSettingsDefaultNetworkModel
	diags.Append(obj.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}
	body := map[string]any{"kind": model.Kind.ValueString()}
	putOptionalString(body, "subnet_id", model.SubnetID)
	putOptionalString(body, "vpc_subnet_id", model.VPCSubnetID)
	return body, diags
}

// microvmSettingsStateFromAPI builds state from the GET/PUT envelope
// ({"default_network": {...} | null}). The id is always the fixed singleton
// value - there is nothing else to key state on.
func microvmSettingsStateFromAPI(envelope map[string]any) (microvmSettingsModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	state := microvmSettingsModel{ID: types.StringValue(microvmSettingsSingletonID)}

	raw, ok := envelope["default_network"].(map[string]any)
	if !ok {
		state.DefaultNetwork = types.ObjectNull(microvmSettingsDefaultNetworkTypes())
		return state, diags
	}

	obj, objDiags := types.ObjectValue(microvmSettingsDefaultNetworkTypes(), map[string]attr.Value{
		"kind":          nullableStringAttr(raw["kind"]),
		"subnet_id":     nullableStringAttr(raw["subnet_id"]),
		"vpc_subnet_id": nullableStringAttr(raw["vpc_subnet_id"]),
	})
	diags.Append(objDiags...)
	state.DefaultNetwork = obj
	return state, diags
}
