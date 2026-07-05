package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// TestAccDNSRecord_basic - LIVE acceptance test (manual staging gate).
func TestAccDNSRecord_basic(t *testing.T) {
	t.Skip("TestAccDNSRecord_basic: acceptance test runs only with TF_ACC + a real record-set id (manual gate)")
}

// TestUnitDNSRecord_lifecycle drives the full GRANDCHILD lifecycle (including the
// inline health_check block) against a stateful mock:
//
//  1. Create - POST .../records (asserts body) + POST .../health-check (asserts
//     body); both reflected in the zone SHOW embedded record_sets[].records[].
//  2. Read - two-level scan of the zone SHOW.
//  3. Import - composite id "<zone_id>/<record_set_id>/<record_id>".
//  4. Update - PATCH the record value + re-STORE the changed health check; asserts
//     both fired.
//  5. Delete - removes the record from the embedded array.
func TestUnitDNSRecord_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		zoneID = "11111111-1111-1111-1111-111111111111"
		rsID   = "22222222-2222-2222-2222-222222222222"
		recID  = "33333333-3333-3333-3333-333333333333"
	)

	var mu sync.Mutex
	exists := false
	value := "10.0.0.1"
	var hc map[string]any // nil when no health check

	recordObj := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var hcCopy any
		if hc != nil {
			hcCopy = hc
		}
		return map[string]any{
			"id":            recID,
			"value":         value,
			"weight":        nil,
			"failover_role": nil,
			"enabled":       true,
			"is_healthy":    true,
			"health_check":  hcCopy,
		}
	}

	embeddedRecords := func() []any {
		mu.Lock()
		e := exists
		mu.Unlock()
		if !e {
			return []any{}
		}
		return []any{recordObj()}
	}

	// Zone SHOW - embeds record_sets[].records[] each with health_check.
	srv.Handle("GET", "/dns-zone/"+zoneID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"zone": map[string]any{
				"id":     zoneID,
				"name":   "corp.internal",
				"status": "active",
				"vpcs":   []any{},
				"record_sets": []any{
					map[string]any{
						"id": rsID, "name": "www", "type": "A", "routing_policy": "simple", "ttl": 300,
						"records": embeddedRecords(),
					},
				},
			},
		})
	})

	// CREATE record.
	srv.Handle("POST", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/records", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		exists = true
		if v, ok := body["value"].(string); ok {
			value = v
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Record created successfully.",
			"record": map[string]any{"id": recID, "value": value, "enabled": true, "is_healthy": true},
		})
	})

	// UPDATE record.
	srv.Handle("PATCH", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["value"].(string); ok {
			value = v
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Record updated successfully.",
			"record": map[string]any{"id": recID, "value": value, "enabled": true, "is_healthy": true},
		})
	})

	// STORE health check (create-or-update).
	srv.Handle("POST", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID+"/health-check", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hc = map[string]any{
			"id":                  "hc-1",
			"type":                body["type"],
			"port":                body["port"],
			"path":                body["path"],
			"expected_status":     body["expected_status"],
			"interval":            float64(30),
			"timeout":             float64(5),
			"unhealthy_threshold": float64(3),
			"healthy_threshold":   float64(2),
		}
		stored := hc
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Health check created successfully.", "health_check": stored})
	})

	// DELETE health check.
	srv.Handle("DELETE", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID+"/health-check", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hc = nil
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Health check deleted successfully."})
	})

	// DELETE record.
	srv.Handle("DELETE", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		hc = nil
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Record deleted successfully."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_dns_record" "test" {
  zone_id       = "` + zoneID + `"
  record_set_id = "` + rsID + `"
  value         = "10.0.0.1"

  health_check = {
    type = "http"
    port = 80
    path = "/health"
  }
}
`
	updateCfg := providerCfg + `
resource "iaas_dns_record" "test" {
  zone_id       = "` + zoneID + `"
  record_set_id = "` + rsID + `"
  value         = "10.0.0.2"

  health_check = {
    type = "http"
    port = 8080
    path = "/healthz"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_record.test", "id", recID),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "zone_id", zoneID),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "record_set_id", rsID),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "value", "10.0.0.1"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "enabled", "true"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "is_healthy", "true"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "health_check.type", "http"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "health_check.port", "80"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "health_check.path", "/health"),
				),
			},
			{
				ResourceName:      "iaas_dns_record.test",
				ImportState:       true,
				ImportStateId:     zoneID + "/" + rsID + "/" + recID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_record.test", "value", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "health_check.port", "8080"),
					resource.TestCheckResourceAttr("iaas_dns_record.test", "health_check.path", "/healthz"),
				),
			},
		},
	})

	// Assert the record CREATE body.
	creates := srv.Requests("POST", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/records")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../records")
	}
	var createBody map[string]any
	_ = json.Unmarshal(creates[0].Body, &createBody)
	if createBody["value"] != "10.0.0.1" {
		t.Errorf("record create body = %v; want value=10.0.0.1", createBody)
	}
	for _, stray := range []string{"id", "zone_id", "record_set_id", "health_check"} {
		if _, present := createBody[stray]; present {
			t.Errorf("record create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert at least two health-check STORE calls fired (create + update) with the
	// correct bodies.
	hcStores := srv.Requests("POST", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID+"/health-check")
	if len(hcStores) < 2 {
		t.Fatalf("expected >=2 health-check STORE calls (create + update), got %d", len(hcStores))
	}
	var firstHC, lastHC map[string]any
	_ = json.Unmarshal(hcStores[0].Body, &firstHC)
	_ = json.Unmarshal(hcStores[len(hcStores)-1].Body, &lastHC)
	if firstHC["type"] != "http" || firstHC["port"] != float64(80) || firstHC["path"] != "/health" {
		t.Errorf("first health-check body = %v; want type=http port=80 path=/health", firstHC)
	}
	if lastHC["port"] != float64(8080) || lastHC["path"] != "/healthz" {
		t.Errorf("updated health-check body = %v; want port=8080 path=/healthz", lastHC)
	}

	// Assert the record PATCH carried the new value.
	patches := srv.Requests("PATCH", "/dns-zone/"+zoneID+"/record-set/"+rsID+"/record/"+recID)
	if len(patches) == 0 {
		t.Fatal("expected a record PATCH")
	}
	var patchBody map[string]any
	_ = json.Unmarshal(patches[len(patches)-1].Body, &patchBody)
	if patchBody["value"] != "10.0.0.2" {
		t.Errorf("record patch body = %v; want value=10.0.0.2", patchBody)
	}
}
