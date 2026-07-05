package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

var (
	_ resource.Resource                = &userScriptResource{}
	_ resource.ResourceWithConfigure   = &userScriptResource{}
	_ resource.ResourceWithImportState = &userScriptResource{}
)

// NewUserScriptResource is the resource constructor registered with the provider.
func NewUserScriptResource() resource.Resource {
	return &userScriptResource{}
}

// userScriptResource manages an iaas_user_script — a reusable cloud-init /
// startup script that can be injected into instances at provision time.
type userScriptResource struct {
	client *client.Client
}

// userScriptModel maps the Terraform state/plan for iaas_user_script.
type userScriptModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Description types.String `tfsdk:"description"`
	Shebang     types.String `tfsdk:"shebang"`
	Content     types.String `tfsdk:"content"`
}

func (r *userScriptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_script"
}

func (r *userScriptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a reusable user script (cloud-init / startup script). The content is " +
			"stored encrypted at rest and can be attached to instances at provision time.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the user script, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name for the script (max 255 characters).",
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "Script type (e.g. `bash`, `cloud-init`). Max 50 characters.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Optional human description of the script.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"shebang": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Optional shebang line (e.g. `#!/bin/bash`). Max 255 characters.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"content": schema.StringAttribute{
				Required:    true,
				Description: "The script body. Stored encrypted at rest by the API.",
			},
		},
	}
}

func (r *userScriptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *userScriptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.CreateUserScript(ctx, userScriptFields(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating user script", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, userScriptStateFromAPI(obj, plan))...)
}

func (r *userScriptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetUserScript(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading user script", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, userScriptStateFromAPI(obj, state))...)
}

func (r *userScriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan userScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.UpdateUserScript(ctx, plan.ID.ValueString(), userScriptFields(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating user script", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, userScriptStateFromAPI(obj, plan))...)
}

func (r *userScriptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteUserScript(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting user script", err))
		return
	}
}

func (r *userScriptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// userScriptFields builds the request body sent on create/update. Optional
// fields (description, shebang) are always sent (empty string clears them),
// matching the PATCH semantics of $request->only([...]).
func userScriptFields(m userScriptModel) map[string]any {
	return map[string]any{
		"name":        m.Name.ValueString(),
		"type":        m.Type.ValueString(),
		"description": m.Description.ValueString(),
		"shebang":     m.Shebang.ValueString(),
		"content":     m.Content.ValueString(),
	}
}

// userScriptStateFromAPI builds the model from an API script object, falling
// back to the prior model's value for any field the response omits.
func userScriptStateFromAPI(obj map[string]any, prior userScriptModel) userScriptModel {
	return userScriptModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		Type:        stringFromAPI(obj, "type", prior.Type),
		Description: stringFromAPI(obj, "description", prior.Description),
		Shebang:     stringFromAPI(obj, "shebang", prior.Shebang),
		Content:     stringFromAPI(obj, "content", prior.Content),
	}
}
