package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// effectiveLocationID returns the canonical location id from a config/plan pair.
// location_id wins when set (mirroring the API contract); otherwise the
// deprecated hypervisor_group_id alias is used so existing configs keep working
// for the one-release overlap.
func effectiveLocationID(locationID, hypervisorGroupID types.String) string {
	if !locationID.IsNull() && !locationID.IsUnknown() && locationID.ValueString() != "" {
		return locationID.ValueString()
	}
	if !hypervisorGroupID.IsNull() && !hypervisorGroupID.IsUnknown() {
		return hypervisorGroupID.ValueString()
	}
	return ""
}

// locationIDFromAPI reads the canonical location id from an API object. The
// Master LocationId response middleware (spec 17 C2) renames hypervisor_group_id
// to location_id in user-facing responses, so location_id is the primary key; we
// fall back to the deprecated hypervisor_group_id key for hosts whose response
// middleware is not yet active, then to the prior value. Present-null collapses
// to "" (stringFromAPI semantics), matching the read pattern used by resources
// whose placement value is always supplied.
func locationIDFromAPI(obj map[string]any, prior types.String) types.String {
	if _, ok := obj["location_id"]; ok {
		return stringFromAPI(obj, "location_id", prior)
	}
	if _, ok := obj["hypervisor_group_id"]; ok {
		return stringFromAPI(obj, "hypervisor_group_id", prior)
	}
	return prior
}

// locationIDOptionalFromAPI mirrors locationIDFromAPI for Optional
// (optionally-derived) attributes, where a present-null collapses to null
// (optionalStringFromAPI semantics) so an unset optional round-trips as null.
func locationIDOptionalFromAPI(obj map[string]any, prior types.String) types.String {
	if _, ok := obj["location_id"]; ok {
		return optionalStringFromAPI(obj, "location_id", prior)
	}
	if _, ok := obj["hypervisor_group_id"]; ok {
		return optionalStringFromAPI(obj, "hypervisor_group_id", prior)
	}
	return prior
}

// hypervisorGroupIDFromAPI maps the deprecated hypervisor_group_id attribute in
// state. When the user configured the legacy key (prior value present) that
// value is authoritative and preserved verbatim, so a superseded legacy value
// never triggers a provider-produced-inconsistent-result after apply (the
// canonical location_id already carries the API winner). On import (no prior)
// it falls back to the API value so a legacy config imports cleanly.
func hypervisorGroupIDFromAPI(obj map[string]any, prior types.String) types.String {
	if !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() != "" {
		return prior
	}
	return locationIDFromAPI(obj, prior)
}

// locationIDFromAliasModifier is a plan modifier for the canonical location_id
// attribute. When location_id is NOT configured but the deprecated
// hypervisor_group_id alias IS configured, it copies the alias value into the
// location_id plan so the canonical attribute always carries the effective
// location id, and the legacy CLI input keeps working with no plan diff.
type locationIDFromAliasModifier struct{}

var _ planmodifier.String = locationIDFromAliasModifier{}

func (m locationIDFromAliasModifier) Description(_ context.Context) string {
	return "Copies the deprecated hypervisor_group_id alias into location_id when location_id is not configured."
}

func (m locationIDFromAliasModifier) MarkdownDescription(_ context.Context) string {
	return m.Description(context.Background())
}

func (m locationIDFromAliasModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Only fold the alias in when the canonical location_id is unset.
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && req.ConfigValue.ValueString() != "" {
		return
	}
	var alias types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("hypervisor_group_id"), &alias)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if alias.IsNull() || alias.IsUnknown() || alias.ValueString() == "" {
		return
	}
	resp.PlanValue = types.StringValue(alias.ValueString())
}
