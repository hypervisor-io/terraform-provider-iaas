package resources

import (
	"context"
	"errors"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"strings"
)

var (
	_ resource.Resource                = &volumeProtectionPolicyAttachmentResource{}
	_ resource.ResourceWithConfigure   = &volumeProtectionPolicyAttachmentResource{}
	_ resource.ResourceWithImportState = &volumeProtectionPolicyAttachmentResource{}
)

func NewVolumeProtectionPolicyAttachmentResource() resource.Resource {
	return &volumeProtectionPolicyAttachmentResource{}
}

type volumeProtectionPolicyAttachmentResource struct{ client *client.Client }
type volumeProtectionPolicyAttachmentModel struct {
	ID       types.String `tfsdk:"id"`
	PolicyID types.String `tfsdk:"policy_id"`
	VolumeID types.String `tfsdk:"volume_id"`
}

func (r *volumeProtectionPolicyAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_protection_policy_attachment"
}
func (r *volumeProtectionPolicyAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Attaches one standalone volume to a protection policy. Each volume can belong to one policy. Deleting the attachment keeps existing backups and snapshots. Other volumes on the policy are unaffected. Import as policy_uuid/volume_uuid.", Attributes: map[string]schema.Attribute{
		"id":        schema.StringAttribute{Computed: true, Description: "policy_uuid/volume_uuid", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"policy_id": schema.StringAttribute{Required: true, Description: "Volume protection policy UUID. Changing it replaces this attachment.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"volume_id": schema.StringAttribute{Required: true, Description: "Volume UUID. Changing it replaces this attachment.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
	}}
}
func (r *volumeProtectionPolicyAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *volumeProtectionPolicyAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p volumeProtectionPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p.ID = types.StringValue(p.PolicyID.ValueString() + "/" + p.VolumeID.ValueString())
	err := r.client.AttachVolumeToProtectionPolicy(ctx, p.PolicyID.ValueString(), p.VolumeID.ValueString())
	if err != nil {
		var apiErr *client.APIError
		// A lost acknowledgement may have attached the volume. Keep an addressable
		// state so the next refresh checks membership instead of losing ownership.
		if !errors.As(err, &apiErr) || apiErr.Status >= 500 {
			resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		}
		resp.Diagnostics.Append(diagFromErr("Error attaching volume protection policy", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}
func (r *volumeProtectionPolicyAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var p volumeProtectionPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetVolumeProtectionPolicy(ctx, p.PolicyID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading volume policy attachment", err))
		return
	}
	members, ok := obj["volumes"].([]any)
	if !ok {
		resp.Diagnostics.AddError("Invalid policy response", "Policy detail omitted the volumes array; attachment state was preserved.")
		return
	}
	found := false
	for _, member := range members {
		m, ok := member.(map[string]any)
		if !ok {
			resp.Diagnostics.AddError("Invalid policy response", "Malformed volume member; attachment state was preserved.")
			return
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			resp.Diagnostics.AddError("Invalid policy response", "Volume member omitted its UUID; attachment state was preserved.")
			return
		}
		if id == p.VolumeID.ValueString() {
			found = true
		}
	}
	if found {
		resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		return
	}
	resp.State.RemoveResource(ctx)
}
func (r *volumeProtectionPolicyAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected attachment update", "Changes to policy_id or volume_id require replacement.")
}
func (r *volumeProtectionPolicyAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var p volumeProtectionPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DetachVolumeFromProtectionPolicy(ctx, p.PolicyID.ValueString(), p.VolumeID.ValueString())
	var apiErr *client.APIError
	if err == nil || client.IsNotFound(err) || (errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Reason == "not_attached") {
		return
	}
	resp.Diagnostics.Append(diagFromErr("Error detaching volume protection policy", err))
}
func (r *volumeProtectionPolicyAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		resp.Diagnostics.AddError("Invalid attachment import ID", "Use policy_uuid/volume_uuid.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), parts[1])...)
}
