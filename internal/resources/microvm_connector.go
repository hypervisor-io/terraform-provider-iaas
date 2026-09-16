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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

var (
	_ resource.Resource                = &microvmConnectorResource{}
	_ resource.ResourceWithConfigure   = &microvmConnectorResource{}
	_ resource.ResourceWithImportState = &microvmConnectorResource{}
)

func NewMicrovmConnectorResource() resource.Resource {
	return &microvmConnectorResource{}
}

type microvmConnectorResource struct {
	client *client.Client
}

type microvmConnectorModel struct {
	ID                 types.String `tfsdk:"id"`
	Kind               types.String `tfsdk:"kind"`
	Name               types.String `tfsdk:"name"`
	Labels             types.List   `tfsdk:"labels"`
	HypervisorGroupID  types.String `tfsdk:"hypervisor_group_id"`
	PlanID             types.String `tfsdk:"plan_id"`
	ImageID            types.String `tfsdk:"image_id"`
	MaxConcurrent      types.Int64  `tfsdk:"max_concurrent"`
	WarmCount          types.Int64  `tfsdk:"warm_count"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	GitSourceID        types.String `tfsdk:"git_source_id"`
	GitlabURL          types.String `tfsdk:"gitlab_url"`
	GitlabToken        types.String `tfsdk:"gitlab_token"`
	GitlabRunUntagged  types.Bool   `tfsdk:"gitlab_run_untagged"`
	State              types.String `tfsdk:"state"`
	BillingSuspendedAt types.String `tfsdk:"billing_suspended_at"`
}

func (r *microvmConnectorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_connector"
}

func (r *microvmConnectorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceString := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages a GitHub App or GitLab integration that launches MicroVMs for provider jobs.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "UUID assigned to the connector.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"kind": schema.StringAttribute{
				Required:      true,
				Description:   "Connector kind: github_app or gitlab_runner.",
				Validators:    []validator.String{stringvalidator.OneOf("github_app", "gitlab_runner")},
				PlanModifiers: replaceString,
			},
			"name":                 schema.StringAttribute{Required: true, Description: "Connector display name."},
			"labels":               schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "GitHub labels or GitLab tag list."},
			"hypervisor_group_id":  schema.StringAttribute{Optional: true, Description: "Location UUID used for launched MicroVMs."},
			"plan_id":              schema.StringAttribute{Optional: true, Description: "Plan UUID used for launched MicroVMs."},
			"image_id":             schema.StringAttribute{Optional: true, Description: "Ready image UUID used for launched MicroVMs."},
			"max_concurrent":       schema.Int64Attribute{Optional: true, Computed: true, Description: "Maximum concurrent jobs. The server defaults to 1."},
			"warm_count":           schema.Int64Attribute{Optional: true, Computed: true, Description: "Warm MicroVM count. The server defaults to 0."},
			"enabled":              schema.BoolAttribute{Optional: true, Computed: true, Description: "Whether the connector accepts jobs. Enabling requires a location and plan.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"git_source_id":        schema.StringAttribute{Optional: true, Description: "Installed owned GitHub App source UUID. Required for github_app.", PlanModifiers: replaceString},
			"gitlab_url":           schema.StringAttribute{Optional: true, Description: "GitLab base URL. Required for gitlab_runner.", PlanModifiers: replaceString},
			"gitlab_token":         schema.StringAttribute{Optional: true, Sensitive: true, Description: "Write-only GitLab runner authentication token. Required when creating gitlab_runner.", PlanModifiers: replaceString},
			"gitlab_run_untagged":  schema.BoolAttribute{Optional: true, Description: "Whether GitLab jobs without tags may use this connector.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"state":                schema.StringAttribute{Computed: true, Description: "Connector readiness state: needs_plan or ready."},
			"billing_suspended_at": schema.StringAttribute{Computed: true, Description: "Timestamp when billing suspended this connector, if any."},
		},
	}
}

func (r *microvmConnectorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *microvmConnectorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmConnectorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := microvmConnectorCreateBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var obj map[string]any
	var err error
	if plan.Kind.ValueString() == "github_app" {
		obj, err = r.client.CreateGithubMicrovmConnector(ctx, body)
	} else {
		obj, err = r.client.CreateGitlabMicrovmConnector(ctx, body)
	}
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MicroVM connector", err))
		return
	}
	state := microvmConnectorStateFromAPI(ctx, obj, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() && !plan.Enabled.Equal(state.Enabled) {
		updateBody, updateDiags := microvmConnectorUpdateBody(ctx, plan)
		resp.Diagnostics.Append(updateDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		obj, err = r.client.UpdateMicrovmConnector(ctx, state.ID.ValueString(), updateBody)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error enabling MicroVM connector", err))
			return
		}
		state = microvmConnectorStateFromAPI(ctx, obj, plan, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

func (r *microvmConnectorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmConnectorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetMicrovmConnector(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM connector", err))
		return
	}
	newState := microvmConnectorStateFromAPI(ctx, obj, state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *microvmConnectorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmConnectorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := microvmConnectorUpdateBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.UpdateMicrovmConnector(ctx, plan.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM connector", err))
		return
	}
	state := microvmConnectorStateFromAPI(ctx, obj, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *microvmConnectorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmConnectorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovmConnector(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting MicroVM connector", err))
	}
}

func (r *microvmConnectorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func microvmConnectorCreateBody(ctx context.Context, plan microvmConnectorModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{"name": plan.Name.ValueString()}
	putOptionalString(body, "hypervisor_group_id", plan.HypervisorGroupID)
	putOptionalString(body, "plan_id", plan.PlanID)
	putOptionalString(body, "image_id", plan.ImageID)
	putOptionalInt64(body, "max_concurrent", plan.MaxConcurrent)
	putOptionalInt64(body, "warm_count", plan.WarmCount)
	labels := connectorLabels(ctx, plan.Labels, &diags)

	switch plan.Kind.ValueString() {
	case "github_app":
		if plan.GitSourceID.IsNull() || plan.GitSourceID.ValueString() == "" {
			diags.AddError("Invalid GitHub connector", "git_source_id is required when kind is github_app")
			return body, diags
		}
		body["git_source_id"] = plan.GitSourceID.ValueString()
		if labels != nil {
			body["labels"] = labels
		}
	case "gitlab_runner":
		if plan.GitlabURL.IsNull() || plan.GitlabURL.ValueString() == "" || plan.GitlabToken.IsNull() || plan.GitlabToken.ValueString() == "" {
			diags.AddError("Invalid GitLab connector", "gitlab_url and gitlab_token are required when kind is gitlab_runner")
			return body, diags
		}
		body["gitlab_url"] = plan.GitlabURL.ValueString()
		body["gitlab_token"] = plan.GitlabToken.ValueString()
		if labels != nil {
			body["gitlab_tag_list"] = labels
		}
		putOptionalBool(body, "gitlab_run_untagged", plan.GitlabRunUntagged)
	default:
		diags.AddError("Invalid MicroVM connector", fmt.Sprintf("unsupported connector kind %q", plan.Kind.ValueString()))
	}
	return body, diags
}

func microvmConnectorUpdateBody(ctx context.Context, plan microvmConnectorModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}
	putOptionalString(body, "name", plan.Name)
	putOptionalString(body, "hypervisor_group_id", plan.HypervisorGroupID)
	putOptionalString(body, "plan_id", plan.PlanID)
	putOptionalString(body, "image_id", plan.ImageID)
	putOptionalInt64(body, "max_concurrent", plan.MaxConcurrent)
	putOptionalInt64(body, "warm_count", plan.WarmCount)
	putOptionalBool(body, "enabled", plan.Enabled)
	if labels := connectorLabels(ctx, plan.Labels, &diags); labels != nil {
		body["labels"] = labels
	}
	return body, diags
}

func connectorLabels(ctx context.Context, labels types.List, diags *diag.Diagnostics) []string {
	if labels.IsNull() || labels.IsUnknown() {
		return nil
	}
	var values []string
	diags.Append(labels.ElementsAs(ctx, &values, false)...)
	return values
}

func microvmConnectorStateFromAPI(ctx context.Context, obj map[string]any, prior microvmConnectorModel, diags *diag.Diagnostics) microvmConnectorModel {
	state := prior
	state.ID = stringFromAPI(obj, "id", prior.ID)
	state.Kind = stringFromAPI(obj, "kind", prior.Kind)
	state.Name = stringFromAPI(obj, "name", prior.Name)
	state.HypervisorGroupID = optionalStringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID)
	state.PlanID = optionalStringFromAPI(obj, "plan_id", prior.PlanID)
	state.ImageID = optionalStringFromAPI(obj, "image_id", prior.ImageID)
	state.MaxConcurrent = int64FromAPI(obj, "max_concurrent", prior.MaxConcurrent)
	state.WarmCount = int64FromAPI(obj, "warm_count", prior.WarmCount)
	state.Enabled = boolFromIntAPI(obj, "enabled", prior.Enabled)
	state.State = stringFromAPI(obj, "state", prior.State)
	state.BillingSuspendedAt = optionalStringFromAPI(obj, "billing_suspended_at", prior.BillingSuspendedAt)
	if raw, exists := obj["labels"]; exists {
		items, ok := raw.([]any)
		if ok {
			values := make([]attr.Value, 0, len(items))
			for _, item := range items {
				if label, ok := item.(string); ok {
					values = append(values, types.StringValue(label))
				}
			}
			list, labelDiags := types.ListValue(types.StringType, values)
			diags.Append(labelDiags...)
			state.Labels = list
		}
	}
	return state
}
