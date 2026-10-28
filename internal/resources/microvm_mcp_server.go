package resources

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

type microvmMcpServerResource struct{ client *client.Client }
type microvmMcpServerModel struct {
	ID                 types.String `tfsdk:"id"`
	Provider           types.String `tfsdk:"provider_slug"`
	Name               types.String `tfsdk:"name"`
	Label              types.String `tfsdk:"label"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	URL                types.String `tfsdk:"url"`
	AuthKind           types.String `tfsdk:"auth_kind"`
	Headers            types.Map    `tfsdk:"headers"`
	StaticToken        types.String `tfsdk:"static_token"`
	StaticTokenVersion types.Int64  `tfsdk:"static_token_version"`
	CredentialState    types.String `tfsdk:"credential_state"`
	RevisionID         types.String `tfsdk:"revision_id"`
	RevisionNumber     types.Int64  `tfsdk:"revision_number"`
	Resource           types.String `tfsdk:"resource"`
}

func NewMicrovmMcpServerResource() resource.Resource { return &microvmMcpServerResource{} }
func (r *microvmMcpServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_mcp_server"
}
func (r *microvmMcpServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Manages an account MCP server used by sandbox gateways. OAuth sign-in remains a separate, person-bound browser action. Identity edits and deletion are refused while active sandbox bindings exist. Requires OpenTofu 1.11 or newer (write-only arguments); static tokens never enter state and are never returned by the API.", Attributes: map[string]schema.Attribute{
		"id":                   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Server UUID. Import by UUID."},
		"provider_slug":        schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Catalog slug or custom. Immutable."},
		"name":                 schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`), "must be 1 to 32 lowercase letters, digits or hyphens, starting with a letter or digit")}, Description: "Unique account key used in agent configuration. Immutable."},
		"label":                schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}, Description: "Display label."},
		"enabled":              schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Whether the server is enabled. Default true. Disabling invalidates bindings."},
		"url":                  schema.StringAttribute{Optional: true, Computed: true, Description: "Custom HTTPS endpoint; required for custom, resolved from catalog otherwise. Preserve path, query and trailing slash."},
		"auth_kind":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("oauth"), Validators: []validator.String{stringvalidator.OneOf("oauth", "bearer", "none")}, Description: "oauth (default), bearer, or none (custom only)."},
		"headers":              schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "Nonsecret custom endpoint headers. At most 8, values at most 1024 bytes. Credential headers are refused. Set {} to clear."},
		"static_token":         schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 8192)}, Description: "Bearer token, required for bearer creation, switching into bearer, or token rotation. Never stored in state or returned. Increment static_token_version to rotate."},
		"static_token_version": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1), Validators: []validator.Int64{int64validator.AtLeast(1)}, Description: "Nonsecret rotation trigger, default 1. Increment to call rotate-token without changing server identity."},
		"credential_state":     schema.StringAttribute{Computed: true, Description: "Credential availability for this API token's acting person. Does not prove gateway readiness."},
		"revision_id":          schema.StringAttribute{Computed: true, Description: "Current immutable destination revision UUID."},
		"revision_number":      schema.Int64Attribute{Computed: true, Description: "Current destination revision number."},
		"resource":             schema.StringAttribute{Computed: true, Description: "OAuth resource resolved by the server; not a writable API field."},
	}}
}
func (r *microvmMcpServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = microvmManagedClient(req, &resp.Diagnostics)
}
func (r *microvmMcpServerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c microvmMcpServerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for key, value := range map[string]types.String{"label": c.Label, "url": c.URL} {
		if !value.IsNull() && !value.IsUnknown() && value.ValueString() != strings.TrimSpace(value.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root(key), "Invalid surrounding whitespace", "Remove surrounding whitespace; the API trims this field.")
		}
	}
	if !c.Provider.IsUnknown() {
		if c.Provider.ValueString() == "custom" {
			if c.URL.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing custom URL", "A custom server requires its HTTPS endpoint.")
			}
		} else {
			if !c.URL.IsNull() || !c.Headers.IsNull() {
				resp.Diagnostics.AddError("Catalog identity is managed by the API", "url and headers may be configured only for provider custom.")
			}
		}
	}
	if !c.AuthKind.IsUnknown() && !c.StaticToken.IsNull() && c.AuthKind.ValueString() != "bearer" {
		resp.Diagnostics.AddAttributeError(path.Root("static_token"), "Token requires bearer authentication", "Set auth_kind to bearer when supplying static_token.")
	}
}
func microvmMcpHeaders(v types.Map) map[string]any {
	m := map[string]any{}
	for k, value := range v.Elements() {
		if s, ok := value.(types.String); ok {
			m[k] = s.ValueString()
		}
	}
	return m
}
func microvmMcpValidate(row map[string]any) error {
	for _, field := range []string{"id", "provider", "name", "label", "credential_state"} {
		if value, ok := row[field].(string); !ok || value == "" {
			return fmt.Errorf("invalid MCP server metadata: missing or malformed %s", field)
		}
	}
	if _, ok := row["enabled"].(bool); !ok {
		return fmt.Errorf("invalid MCP server metadata: missing or malformed enabled")
	}
	raw, present := row["revision"]
	if !present {
		return fmt.Errorf("invalid MCP server metadata: missing revision")
	}
	if raw == nil {
		return nil
	}
	revision, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid MCP server metadata: malformed revision")
	}
	for _, field := range []string{"id", "url", "auth_kind"} {
		if value, ok := revision[field].(string); !ok || value == "" {
			return fmt.Errorf("invalid MCP server revision: missing or malformed %s", field)
		}
	}
	if value, ok := revision["revision"].(float64); !ok || value < 1 || math.Trunc(value) != value {
		return fmt.Errorf("invalid MCP server revision number")
	}
	if value := revision["resource"]; value != nil {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("invalid MCP server resource")
		}
	}
	switch headers := revision["headers"].(type) {
	case map[string]any:
		for _, value := range headers {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("invalid MCP server header")
			}
		}
	case []any:
		if len(headers) != 0 {
			return fmt.Errorf("invalid MCP server headers")
		}
	default:
		return fmt.Errorf("invalid MCP server headers")
	}
	return nil
}
func microvmMcpApply(model *microvmMcpServerModel, row map[string]any) {
	model.ID = microvmManagedString(row["id"])
	model.Provider = microvmManagedString(row["provider"])
	model.Name = microvmManagedString(row["name"])
	model.Label = microvmManagedString(row["label"])
	enabled, _ := row["enabled"].(bool)
	model.Enabled = types.BoolValue(enabled)
	model.CredentialState = microvmManagedString(row["credential_state"])
	revision, _ := row["revision"].(map[string]any)
	model.RevisionID = microvmManagedString(revision["id"])
	model.URL = microvmManagedString(revision["url"])
	model.AuthKind = microvmManagedString(revision["auth_kind"])
	model.Resource = microvmManagedString(revision["resource"])
	number, _ := revision["revision"].(float64)
	model.RevisionNumber = types.Int64Value(int64(number))
	if revision == nil {
		model.RevisionNumber = types.Int64Null()
	}
	headers := map[string]attr.Value{}
	if raw, ok := revision["headers"].(map[string]any); ok {
		for k, v := range raw {
			headers[k] = microvmManagedString(v)
		}
	}
	model.Headers = types.MapValueMust(types.StringType, headers)
	model.StaticToken = types.StringNull()
	if model.StaticTokenVersion.IsNull() {
		model.StaticTokenVersion = types.Int64Value(1)
	}
}
func (r *microvmMcpServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config microvmMcpServerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"provider": plan.Provider.ValueString(), "name": plan.Name.ValueString(), "label": plan.Label.ValueString(), "enabled": plan.Enabled.ValueBool(), "auth_kind": plan.AuthKind.ValueString()}
	if plan.Provider.ValueString() == "custom" {
		body["url"] = plan.URL.ValueString()
		if !plan.Headers.IsUnknown() && !plan.Headers.IsNull() {
			body["headers"] = microvmMcpHeaders(plan.Headers)
		}
	}
	if plan.AuthKind.ValueString() == "bearer" {
		if !microvmRequireWriteSecret(config.StaticToken, "static_token", &resp.Diagnostics) {
			return
		}
		body["static_token"] = config.StaticToken.ValueString()
	}
	row, err := r.client.CreateMicrovmMcpServer(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MCP server", err))
		return
	}
	if id, _ := row["id"].(string); id == "" {
		resp.Diagnostics.AddError("Invalid MCP server response", "Create omitted the server ID. Check the account before retrying.")
		return
	}
	if validationErr := microvmMcpValidate(row); validationErr != nil {
		// The POST succeeded and supplied an identity. Retain it so a malformed
		// presenter cannot orphan the server or provoke duplicate creation.
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), row["id"].(string))...)
		resp.Diagnostics.AddError("Invalid MCP server response", validationErr.Error())
		return
	}
	microvmMcpApply(&plan, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *microvmMcpServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmMcpServerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.GetMicrovmMcpServer(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MCP server", err))
		return
	}
	if id, _ := row["id"].(string); id == "" {
		resp.Diagnostics.AddError("Invalid MCP server response", "Read omitted the server ID; existing state is retained.")
		return
	}
	if validationErr := microvmMcpValidate(row); validationErr != nil {
		resp.Diagnostics.AddError("Invalid MCP server response", validationErr.Error())
		return
	}
	microvmMcpApply(&state, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
func (r *microvmMcpServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config microvmMcpServerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	toBearer := plan.AuthKind.ValueString() == "bearer" && state.AuthKind.ValueString() != "bearer"
	rotate := !plan.StaticTokenVersion.Equal(state.StaticTokenVersion)
	if toBearer || rotate {
		if plan.AuthKind.ValueString() != "bearer" {
			resp.Diagnostics.AddError("Token rotation requires bearer authentication", "Only change static_token_version for bearer servers.")
			return
		}
		if !microvmRequireWriteSecret(config.StaticToken, "static_token", &resp.Diagnostics) {
			return
		}
	}
	body := map[string]any{}
	if !plan.Label.Equal(state.Label) {
		body["label"] = plan.Label.ValueString()
	}
	if !plan.Enabled.Equal(state.Enabled) {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	if !plan.AuthKind.Equal(state.AuthKind) {
		body["auth_kind"] = plan.AuthKind.ValueString()
	}
	if plan.Provider.ValueString() == "custom" {
		if !plan.URL.IsUnknown() && !plan.URL.Equal(state.URL) {
			body["url"] = plan.URL.ValueString()
		}
		if !plan.Headers.IsUnknown() && !plan.Headers.Equal(state.Headers) {
			body["headers"] = microvmMcpHeaders(plan.Headers)
		}
	}
	if toBearer {
		body["static_token"] = config.StaticToken.ValueString()
	}
	var row map[string]any
	var err error
	if len(body) > 0 {
		row, err = r.client.UpdateMicrovmMcpServer(ctx, state.ID.ValueString(), body)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating MCP server", err))
			return
		}
	}
	if rotate && !toBearer {
		rotated, rotateErr := r.client.RotateMicrovmMcpServerToken(ctx, state.ID.ValueString(), config.StaticToken.ValueString())
		if rotateErr != nil {
			if row != nil {
				plan.StaticTokenVersion = state.StaticTokenVersion
				if validationErr := microvmMcpValidate(row); validationErr != nil {
					resp.Diagnostics.AddError("Invalid MCP server response", validationErr.Error())
					return
				}
				microvmMcpApply(&plan, row)
				resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			}
			resp.Diagnostics.Append(diagFromErr("Error rotating MCP server token", rotateErr))
			return
		}
		row = rotated
	}
	if row == nil {
		row, err = r.client.GetMicrovmMcpServer(ctx, state.ID.ValueString())
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error reading MCP server", err))
			return
		}
	}
	if validationErr := microvmMcpValidate(row); validationErr != nil {
		resp.Diagnostics.AddError("Invalid MCP server response", validationErr.Error())
		return
	}
	microvmMcpApply(&plan, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *microvmMcpServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmMcpServerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovmMcpServer(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("Error deleting MCP server", err))
	}
}
func (r *microvmMcpServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
