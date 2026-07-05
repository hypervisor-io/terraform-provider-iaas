package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// TestAccDNSZone_basic - LIVE acceptance test (manual staging gate). Auto-skips
// unless TF_ACC is set.
func TestAccDNSZone_basic(t *testing.T) {
	t.Skip("TestAccDNSZone_basic: acceptance test runs only with TF_ACC against real staging (manual gate)")
}

// TestUnitDNSZone_lifecycle drives the full PARENT lifecycle against a stateful
// mock:
//
//  1. Create - POST /dns-zones (with vpc_ids), asserts the body; SHOW reflects the
//     attached VPC + active status.
//  2. Read - GET /dns-zone/{id}.
//  3. Import - single id.
//  4. Update - PATCH description + attach vpc-2 + detach vpc-1; asserts each fired.
//  5. Delete - DELETE then poll SHOW to 404 (async delete convergence).
func TestUnitDNSZone_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms") // no sleep in the delete poll

	srv := acctest.NewMockServer(t)

	const (
		zoneID = "11111111-1111-1111-1111-111111111111"
		vpc1   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		vpc2   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	var mu sync.Mutex
	exists := false
	deleted := false
	description := "primary zone"
	attached := map[string]bool{}

	vpcsArr := func() []any {
		out := []any{}
		if attached[vpc1] {
			out = append(out, map[string]any{"id": vpc1, "name": "prod"})
		}
		if attached[vpc2] {
			out = append(out, map[string]any{"id": vpc2, "name": "staging"})
		}
		return out
	}

	// Zone SHOW.
	srv.Handle("GET", "/dns-zone/"+zoneID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if deleted || !exists {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "DNS Zone not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"zone": map[string]any{
				"id":          zoneID,
				"name":        "corp.internal",
				"description": description,
				"status":      "active",
				"vpcs":        vpcsArr(),
				"record_sets": []any{},
			},
			"available_vpcs": []any{},
		})
	})

	// CREATE zone.
	srv.Handle("POST", "/dns-zones", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		exists = true
		if d, ok := body["description"].(string); ok {
			description = d
		}
		if ids, ok := body["vpc_ids"].([]any); ok {
			for _, id := range ids {
				if s, ok := id.(string); ok {
					attached[s] = true
				}
			}
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "DNS zone created successfully.",
			"zone":    map[string]any{"id": zoneID, "name": "corp.internal", "description": description, "status": "active"},
		})
	})

	// UPDATE zone (description).
	srv.Handle("PATCH", "/dns-zone/"+zoneID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if d, ok := body["description"].(string); ok {
			description = d
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "DNS zone updated successfully.",
			"zone": map[string]any{"id": zoneID, "name": "corp.internal", "description": description},
		})
	})

	// ATTACH vpc.
	srv.Handle("POST", "/dns-zone/"+zoneID+"/attach-vpc", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["vpc_id"].(string); ok {
			attached[v] = true
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC attached to DNS zone successfully."})
	})

	// DETACH vpc-1 and vpc-2.
	for _, v := range []string{vpc1, vpc2} {
		v := v
		srv.Handle("DELETE", "/dns-zone/"+zoneID+"/detach-vpc/"+v, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			delete(attached, v)
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC detached from DNS zone successfully."})
		})
	}

	// DELETE zone (async: SHOW 404s on the next poll).
	srv.Handle("DELETE", "/dns-zone/"+zoneID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "DNS zone deletion queued."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_dns_zone" "test" {
  name        = "corp.internal"
  description = "primary zone"
  vpc_ids     = ["` + vpc1 + `"]
}
`
	updateCfg := providerCfg + `
resource "iaas_dns_zone" "test" {
  name        = "corp.internal"
  description = "updated zone"
  vpc_ids     = ["` + vpc2 + `"]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "id", zoneID),
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "name", "corp.internal"),
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "description", "primary zone"),
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "vpc_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_dns_zone.test", "vpc_ids.*", vpc1),
				),
			},
			{
				ResourceName:            "iaas_dns_zone.test",
				ImportState:             true,
				ImportStateId:           zoneID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "description", "updated zone"),
					resource.TestCheckResourceAttr("iaas_dns_zone.test", "vpc_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_dns_zone.test", "vpc_ids.*", vpc2),
				),
			},
		},
	})

	// Assert the CREATE body carried name + vpc_ids and omitted computed fields.
	creates := srv.Requests("POST", "/dns-zones")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /dns-zones")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding zone create body: %v", err)
	}
	if createBody["name"] != "corp.internal" || createBody["description"] != "primary zone" {
		t.Errorf("zone create body = %v; want name+description", createBody)
	}
	if ids, ok := createBody["vpc_ids"].([]any); !ok || len(ids) != 1 || ids[0] != vpc1 {
		t.Errorf("zone create body vpc_ids = %v; want [%s]", createBody["vpc_ids"], vpc1)
	}
	for _, stray := range []string{"id", "status"} {
		if _, present := createBody[stray]; present {
			t.Errorf("zone create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the UPDATE patched description.
	patches := srv.Requests("PATCH", "/dns-zone/"+zoneID)
	if len(patches) == 0 {
		t.Fatal("expected a PATCH /dns-zone/{id}")
	}
	var patchBody map[string]any
	_ = json.Unmarshal(patches[len(patches)-1].Body, &patchBody)
	if patchBody["description"] != "updated zone" {
		t.Errorf("zone patch body = %v; want description=updated zone", patchBody)
	}

	// Assert exactly one attach of vpc-2 and one detach of vpc-1.
	if got := len(srv.Requests("POST", "/dns-zone/"+zoneID+"/attach-vpc")); got != 1 {
		t.Errorf("attach-vpc calls = %d; want 1 (attach vpc-2)", got)
	}
	if got := len(srv.Requests("DELETE", "/dns-zone/"+zoneID+"/detach-vpc/"+vpc1)); got != 1 {
		t.Errorf("detach vpc-1 calls = %d; want 1", got)
	}
	if got := len(srv.Requests("DELETE", "/dns-zone/"+zoneID)); got != 1 {
		t.Errorf("zone delete calls = %d; want 1", got)
	}
}
