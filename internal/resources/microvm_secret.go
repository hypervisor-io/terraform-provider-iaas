package resources

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

type microvmSecretResource struct{ client *client.Client }
type microvmSecretModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Kind         types.String `tfsdk:"kind"`
	Registry     types.String `tfsdk:"registry"`
	Username     types.String `tfsdk:"username"`
	Value        types.String `tfsdk:"value"`
	ValueVersion types.Int64  `tfsdk:"value_version"`
}

func NewMicrovmSecretResource() resource.Resource { return &microvmSecretResource{} }
func (r *microvmSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_secret"
}
func (r *microvmSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Manages an account sandbox environment secret or registry credential. Requires OpenTofu 1.11 or newer (write-only arguments). Values never enter state and cannot be recovered. Increment value_version to rotate value; changing value alone does not schedule an update. Running sandboxes keep their original copy.", Attributes: map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Secret UUID. Import supported by UUID."},
		"name":          schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 100), stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`), "must be an uppercase environment variable name")}, Description: "Unique account environment variable name."},
		"kind":          schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("env"), Validators: []validator.String{stringvalidator.OneOf("env", "registry")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "env (default) or registry. Immutable."},
		"registry":      schema.StringAttribute{Optional: true, Description: "Required for registry credentials, absent for env. Use a normalized host:port; Docker Hub aliases must be written as docker.io. Changes are refused while a kit references the credential."},
		"username":      schema.StringAttribute{Optional: true, Description: "Required for registry credentials, absent for env; can be rotated."},
		"value":         schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 8192)}, Description: "Write-only value, required at creation or when value_version changes. Whitespace is significant. Never stored in state."},
		"value_version": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1), Validators: []validator.Int64{int64validator.AtLeast(1)}, Description: "Increment to rotate value. Nonsecret rotation trigger, default 1."},
	}}
}
func (r *microvmSecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = microvmManagedClient(req, &resp.Diagnostics)
}
func microvmManagedClient(req resource.ConfigureRequest, diags *diag.Diagnostics) *client.Client {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return nil
	}
	return c
}
func (r *microvmSecretResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config microvmSecretModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.Kind.IsUnknown() {
		return
	}
	kind := config.Kind.ValueString()
	if kind == "" {
		kind = "env"
	}
	if !config.Registry.IsNull() && !config.Registry.IsUnknown() {
		v := config.Registry.ValueString()
		if v != strings.ToLower(v) || v != strings.TrimSpace(v) || v == "index.docker.io" || v == "registry-1.docker.io" || v == "registry.hub.docker.com" {
			resp.Diagnostics.AddAttributeError(path.Root("registry"), "Use a normalized registry", "Use a lowercase host with no surrounding whitespace; use docker.io for Docker Hub aliases.")
		}
	}
	if !config.Username.IsNull() && !config.Username.IsUnknown() && config.Username.ValueString() != strings.TrimSpace(config.Username.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("username"), "Invalid username whitespace", "Remove surrounding whitespace; the API trims usernames.")
	}
	if kind == "registry" {
		for name, value := range map[string]types.String{"registry": config.Registry, "username": config.Username} {
			if value.IsNull() || (!value.IsUnknown() && value.ValueString() == "") {
				resp.Diagnostics.AddAttributeError(path.Root(name), "Missing registry credential field", name+" is required when kind is registry.")
			}
		}
	} else if !config.Registry.IsNull() || !config.Username.IsNull() {
		resp.Diagnostics.AddError("Invalid environment secret", "registry and username apply only to kind registry.")
	}
}
func microvmSecretBody(model microvmSecretModel) map[string]any {
	body := map[string]any{"name": model.Name.ValueString()}
	if !model.Registry.IsNull() && !model.Registry.IsUnknown() {
		body["registry"] = model.Registry.ValueString()
	}
	if !model.Username.IsNull() && !model.Username.IsUnknown() {
		body["username"] = model.Username.ValueString()
	}
	return body
}
func microvmRequireWriteSecret(value types.String, field string, diags *diag.Diagnostics) bool {
	if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
		diags.AddAttributeError(path.Root(field), "Missing write-only credential", "Supply this credential when creating the resource or changing its version.")
		return false
	}
	return true
}
func (r *microvmSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config microvmSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || !microvmRequireWriteSecret(config.Value, "value", &resp.Diagnostics) {
		return
	}
	body := microvmSecretBody(plan)
	body["kind"] = plan.Kind.ValueString()
	body["value"] = config.Value.ValueString()
	row, err := r.client.CreateMicrovmSecret(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating microVM secret", err))
		return
	}
	id, _ := row["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Invalid secret response", "The create response omitted the secret ID. Check the account before retrying creation.")
		return
	}
	plan.ID = types.StringValue(id)
	plan.Value = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *microvmSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := r.client.ListMicrovmSecrets(ctx, "all")
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading microVM secret", err))
		return
	}
	for _, row := range rows {
		if row["id"] == state.ID.ValueString() {
			if err := microvmSecretValidate(row); err != nil {
				resp.Diagnostics.AddError("Invalid secret response", err.Error())
				return
			}
			state.Name = microvmManagedString(row["name"])
			state.Kind = microvmManagedString(row["kind"])
			state.Registry = microvmManagedString(row["registry"])
			state.Username = microvmManagedString(row["username"])
			state.Value = types.StringNull()
			if state.ValueVersion.IsNull() {
				state.ValueVersion = types.Int64Value(1)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}
func microvmManagedString(v any) types.String {
	if s, ok := v.(string); ok {
		return types.StringValue(s)
	}
	return types.StringNull()
}
func (r *microvmSecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config microvmSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := microvmSecretBody(plan)
	if !plan.ValueVersion.Equal(state.ValueVersion) {
		if !microvmRequireWriteSecret(config.Value, "value", &resp.Diagnostics) {
			return
		}
		body["value"] = config.Value.ValueString()
	}
	_, err := r.client.UpdateMicrovmSecret(ctx, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating microVM secret", err))
		return
	}
	plan.ID = state.ID
	plan.Value = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *microvmSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovmSecret(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("Error deleting microVM secret", err))
	}
}
func (r *microvmSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func microvmSecretValidate(row map[string]any) error {
	for _, field := range []string{"id", "name", "kind"} {
		if value, ok := row[field].(string); !ok || value == "" {
			return fmt.Errorf("invalid secret metadata: missing or malformed %s", field)
		}
	}
	kind := row["kind"].(string)
	if kind != "env" && kind != "registry" {
		return fmt.Errorf("invalid secret metadata: unsupported kind")
	}
	for _, field := range []string{"registry", "username"} {
		value, present := row[field]
		if !present {
			return fmt.Errorf("invalid secret metadata: missing %s", field)
		}
		if value == nil && kind == "env" {
			continue
		}
		if text, ok := value.(string); !ok || text == "" {
			return fmt.Errorf("invalid secret metadata: malformed %s", field)
		}
	}
	return nil
}
