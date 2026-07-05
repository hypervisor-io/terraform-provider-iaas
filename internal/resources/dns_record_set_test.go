package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// TestAccDNSRecordSet_basic - LIVE acceptance test (manual staging gate).
func TestAccDNSRecordSet_basic(t *testing.T) {
	t.Skip("TestAccDNSRecordSet_basic: acceptance test runs only with TF_ACC + a real zone id (manual gate)")
}

// TestUnitDNSRecordSet_lifecycle drives the full CHILD lifecycle against a
// stateful mock:
//
//  1. Create - POST /dns-zone/{zoneId}/record-sets; the record set appears in the
//     zone SHOW embedded record_sets[]. Asserts the create body.
//  2. Read - scans the zone SHOW record_sets[].
//  3. Import - composite id "<zone_id>/<record_set_id>".
//  4. Update - PATCH record-set (routing_policy + ttl); asserts the PATCH body.
//  5. Delete - removes the record set from the embedded array.
func TestUnitDNSRecordSet_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		zoneID = "11111111-1111-1111-1111-111111111111"
		rsID   = "22222222-2222-2222-2222-222222222222"
	)

	var mu sync.Mutex
	exists := false
	routingPolicy := "simple"
	ttl := float64(300)

	embedded := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":             rsID,
				"name":           "www",
				"type":           "A",
				"routing_policy": routingPolicy,
				"ttl":            ttl,
				"records":        []any{},
			},
		}
	}

	// Zone SHOW - embeds record_sets[].
	srv.Handle("GET", "/dns-zone/"+zoneID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"zone": map[string]any{
				"id":          zoneID,
				"name":        "corp.internal",
				"status":      "active",
				"vpcs":        []any{},
				"record_sets": embedded(),
			},
		})
	})

	// CREATE record set.
	srv.Handle("POST", "/dns-zone/"+zoneID+"/record-sets", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Record set created successfully.",
			"record_set": map[string]any{"id": rsID, "name": "www", "type": "A", "routing_policy": "simple", "ttl": 300},
		})
	})

	// UPDATE record set.
	srv.Handle("PATCH", "/dns-zone/"+zoneID+"/record-set/"+rsID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["routing_policy"].(string); ok {
			routingPolicy = v
		}
		if v, ok := body["ttl"].(float64); ok {
			ttl = v
		}
		rp, tv := routingPolicy, ttl
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Record set updated successfully.",
			"record_set": map[string]any{"id": rsID, "name": "www", "type": "A", "routing_policy": rp, "ttl": tv},
		})
	})

	// DELETE record set.
	srv.Handle("DELETE", "/dns-zone/"+zoneID+"/record-set/"+rsID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Record set deleted successfully."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_dns_record_set" "test" {
  zone_id        = "` + zoneID + `"
  name           = "www"
  type           = "A"
  routing_policy = "simple"
  ttl            = 300
}
`
	updateCfg := providerCfg + `
resource "iaas_dns_record_set" "test" {
  zone_id        = "` + zoneID + `"
  name           = "www"
  type           = "A"
  routing_policy = "weighted"
  ttl            = 600
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "id", rsID),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "zone_id", zoneID),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "name", "www"),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "type", "A"),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "routing_policy", "simple"),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "ttl", "300"),
				),
			},
			{
				ResourceName:      "iaas_dns_record_set.test",
				ImportState:       true,
				ImportStateId:     zoneID + "/" + rsID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "routing_policy", "weighted"),
					resource.TestCheckResourceAttr("iaas_dns_record_set.test", "ttl", "600"),
				),
			},
		},
	})

	// Assert the CREATE body and that it omitted server fields.
	creates := srv.Requests("POST", "/dns-zone/"+zoneID+"/record-sets")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../record-sets")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding record-set create body: %v", err)
	}
	if createBody["name"] != "www" || createBody["type"] != "A" ||
		createBody["routing_policy"] != "simple" || createBody["ttl"] != float64(300) {
		t.Errorf("record-set create body = %v; want name=www type=A routing_policy=simple ttl=300", createBody)
	}
	for _, stray := range []string{"id", "zone_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("record-set create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the PATCH body carried the policy + ttl change.
	patches := srv.Requests("PATCH", "/dns-zone/"+zoneID+"/record-set/"+rsID)
	if len(patches) == 0 {
		t.Fatal("expected a PATCH .../record-set/{id}")
	}
	var patchBody map[string]any
	_ = json.Unmarshal(patches[len(patches)-1].Body, &patchBody)
	if patchBody["routing_policy"] != "weighted" || patchBody["ttl"] != float64(600) {
		t.Errorf("record-set patch body = %v; want routing_policy=weighted ttl=600", patchBody)
	}
}
