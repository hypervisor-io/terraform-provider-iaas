package datasources

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// effectiveLocationID returns the canonical location id from a config pair.
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
