package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// This fixture has a non-UTC account owner (Asia/Kolkata). A bare timezone
// mutation changes the presented clock unless the provider also submits its
// configured local clock. The lifecycle detects Terraform inconsistent results.
func TestUnitInstancePolicyGFSTimezoneLifecycle(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)
	var mu sync.Mutex
	policy := map[string]any{"id": "policy-zone", "retention_count": 7, "status": "active", "instances": []any{}}
	var zone any
	storedMinutes := 0
	asClock := func(minutes int) string {
		minutes = (minutes%1440 + 1440) % 1440
		return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
	}
	reply := func() map[string]any {
		copy := map[string]any{}
		for key, value := range policy {
			copy[key] = value
		}
		clock := storedMinutes
		if zone == nil {
			clock += 330
		}
		copy["timezone"] = zone
		copy["full_backup_time"] = asClock(clock)
		return copy
	}
	mutate := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if next, ok := body["timezone"]; ok {
			zone = next
		}
		if clock, ok := body["full_backup_time"].(string); ok {
			var hours, minutes int
			_, _ = fmt.Sscanf(clock, "%d:%d", &hours, &minutes)
			storedMinutes = hours*60 + minutes
			if zone == nil {
				storedMinutes -= 330
			}
		}
		for key, value := range body {
			policy[key] = value
		}
		// Master normalizes disabled keep rules to null.
		if value, ok := policy["keep_daily"].(float64); ok && value == 0 {
			policy["keep_daily"] = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "policy": reply()})
	}
	srv.Handle("POST", "/backup-policies", mutate)
	srv.Handle("PATCH", "/backup-policy/policy-zone", mutate)
	srv.Handle("GET", "/backup-policy/policy-zone", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"policy": reply(), "available_instances": []any{}})
	})
	srv.Handle("DELETE", "/backup-policy/policy-zone", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	})
	config := func(zoneLine string) string {
		return acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_instance_backup_policy" "gfs" {
 name = "nightly"
 full_backup_frequency = "daily"
 full_backup_time = "02:30"
 max_incremental_chain = 0
 backup_device = "all"
 retention_mode = "gfs"
 keep_last = 2
 keep_daily = 0
 ` + zoneLine + `
}
`
	}
	check := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("iaas_instance_backup_policy.gfs", "full_backup_time", "02:30"),
		resource.TestCheckResourceAttr("iaas_instance_backup_policy.gfs", "retention_mode", "gfs"),
		resource.TestCheckResourceAttr("iaas_instance_backup_policy.gfs", "keep_daily", "0"),
	)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: config(`timezone = "Europe/Amsterdam"`), Check: check},
			{Config: config(""), Check: check},
			{Config: config(`timezone = "Asia/Kolkata"`), Check: check},
		},
	})
	patches := srv.Requests("PATCH", "/backup-policy/policy-zone")
	if len(patches) != 2 {
		t.Fatalf("PATCH count=%d", len(patches))
	}
	for _, request := range patches {
		var body map[string]any
		_ = json.Unmarshal(request.Body, &body)
		if body["full_backup_time"] != "02:30" {
			t.Fatalf("zone edit must retain configured clock: %#v", body)
		}
	}
}
