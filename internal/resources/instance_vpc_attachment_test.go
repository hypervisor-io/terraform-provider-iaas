package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// instanceVpcMockServer is a stateful mock of the InstanceVpcController API
// for the lifecycle test. It models a fixed pool of vpc_subnet_ips rows in a
// single subnet (mirroring VPCSubnet::assignIpToInstance's lowest-free-ip-
// first allocation) plus enable/disable/add/remove/primary call counters so
// the test can assert the resource's request sequencing.
// ---------------------------------------------------------------------------

type mockVpcIPRow struct {
	id        string
	ip        string
	free      bool
	isPrimary bool
}

type instanceVpcMockServer struct {
	mu sync.Mutex

	instanceID string
	vpcID      string
	subnetID   string
	enabled    bool

	// pool is ordered ascending by address - index 0 is always the address
	// `enable` auto-assigns first, mirroring the real allocator's
	// lockForUpdate()-ordered-by-ip behaviour.
	pool []*mockVpcIPRow

	enableCalls  int
	disableCalls int
	addCalls     int
	removeCalls  int
	primaryCalls int
}

func newInstanceVpcMockServer(instanceID, vpcID, subnetID string) *instanceVpcMockServer {
	return &instanceVpcMockServer{
		instanceID: instanceID,
		vpcID:      vpcID,
		subnetID:   subnetID,
		pool: []*mockVpcIPRow{
			{id: "vip-1", ip: "10.0.0.2", free: true},
			{id: "vip-2", ip: "10.0.0.3", free: true},
			{id: "vip-3", ip: "10.0.0.4", free: true},
			{id: "vip-4", ip: "10.0.0.5", free: true},
		},
	}
}

func (s *instanceVpcMockServer) rowJSON(row *mockVpcIPRow) map[string]any {
	return map[string]any{
		"id":            row.id,
		"vpc_subnet_id": s.subnetID,
		"instance_id":   s.instanceID,
		"ip":            row.ip,
		"mac":           "aa:bb:cc:dd:ee:ff",
		"is_primary":    row.isPrimary,
		"status":        "used",
		"subnet": map[string]any{
			"id":     s.subnetID,
			"vpc_id": s.vpcID,
			"cidr":   "10.0.0.0/24",
			"vpc":    map[string]any{"id": s.vpcID},
		},
	}
}

// attachedRows returns every currently-attached (non-free) row, ascending by
// address for deterministic test assertions.
func (s *instanceVpcMockServer) attachedRows() []*mockVpcIPRow {
	var out []*mockVpcIPRow
	for _, row := range s.pool {
		if !row.free {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ip < out[j].ip })
	return out
}

func (s *instanceVpcMockServer) findByID(id string) *mockVpcIPRow {
	for _, row := range s.pool {
		if row.id == id {
			return row
		}
	}
	return nil
}

func (s *instanceVpcMockServer) findByIP(ip string) *mockVpcIPRow {
	for _, row := range s.pool {
		if row.ip == ip {
			return row
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// TestUnitInstanceVpcAttachment_lifecycle - MOCK-backed lifecycle proof.
//
// Steps:
//  1. Create with additional_ips = ["10.0.0.3"] → enable auto-assigns the
//     lowest free address (10.0.0.2, primary) and one ip/add call attaches
//     10.0.0.3. Assert auto_assigned_ip, additional_ips, primary_ip, ips.
//  2. Import by instance_id (the attachment's natural key) → ImportStateVerify
//     confirms Read reconstructs the same split (primary hasn't moved since
//     create, so the auto_assigned_ip heuristic recovers the true value).
//  3. Update: ADD 10.0.0.4, REMOVE 10.0.0.3 (net: one more ip/add + one
//     DELETE ip/{id}) → assert the resulting additional_ips/ips reflect the
//     swap and auto_assigned_ip is untouched.
//
// Delete is implicit teardown after the final step; the counters are
// asserted once the whole TestCase (including its final destroy) completes.
// ---------------------------------------------------------------------------
func TestUnitInstanceVpcAttachment_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		instanceID = "55555555-5555-5555-5555-555555555555"
		vpcID      = "66666666-6666-6666-6666-666666666666"
		subnetID   = "77777777-7777-7777-7777-777777777777"
	)

	store := newInstanceVpcMockServer(instanceID, vpcID, subnetID)

	// ENABLE - POST /instance/{id}/vpc/enable.
	srv.Handle("POST", "/instance/"+instanceID+"/vpc/enable", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.enableCalls++
		if v, _ := body["vpc_id"].(string); v != "" {
			store.vpcID = v
		}
		if v, _ := body["vpc_subnet_id"].(string); v != "" {
			store.subnetID = v
		}
		store.enabled = true
		// Auto-assign the lowest free address and mark it primary.
		for _, row := range store.pool {
			if row.free {
				row.free = false
				row.isPrimary = true
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC has been enabled for this instance.",
		})
	})

	// DISABLE - POST /instance/{id}/vpc/disable.
	srv.Handle("POST", "/instance/"+instanceID+"/vpc/disable", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.disableCalls++
		store.enabled = false
		for _, row := range store.pool {
			row.free = true
			row.isPrimary = false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC has been disabled for this instance.",
		})
	})

	// LIST IPS - GET /instance/{id}/vpc/ips → Laravel paginator envelope.
	srv.Handle("GET", "/instance/"+instanceID+"/vpc/ips", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		rows := store.attachedRows()
		data := make([]any, 0, len(rows))
		for _, row := range rows {
			data = append(data, store.rowJSON(row))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"data":         data,
			"last_page":    1,
			"per_page":     10,
			"total":        len(data),
		})
	})

	// AVAILABLE IPS - GET /instance/{id}/vpc/available-ips → bare array or
	// {success:false} when no VPC is attached.
	srv.Handle("GET", "/instance/"+instanceID+"/vpc/available-ips", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		if !store.enabled {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"message": "No VPC is attached to this instance.",
			})
			return
		}
		var free []*mockVpcIPRow
		for _, row := range store.pool {
			if row.free {
				free = append(free, row)
			}
		}
		sort.Slice(free, func(i, j int) bool { return free[i].ip < free[j].ip })
		out := make([]any, 0, len(free))
		for _, row := range free {
			out = append(out, map[string]any{"id": row.id, "ip": row.ip})
		}
		writeJSON(w, http.StatusOK, out)
	})

	// ADD IP - POST /instance/{id}/vpc/ip/add.
	srv.Handle("POST", "/instance/"+instanceID+"/vpc/ip/add", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.addCalls++

		var row *mockVpcIPRow
		if id, _ := body["ip_id"].(string); id != "" {
			row = store.findByID(id)
		} else {
			for _, candidate := range store.pool {
				if candidate.free {
					row = candidate
					break
				}
			}
		}
		if row == nil || !row.free {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"message": "The selected VPC IP is not available.",
			})
			return
		}
		row.free = false
		row.isPrimary = false // ip/add never auto-primaries
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC IP has been added to this instance.",
			"vpc_ip":  store.rowJSON(row),
		})
	})

	// SET PRIMARY + REMOVE - pre-registered per pool id (the mock server
	// dispatches by exact method+path, same approach as ip_set_test.go).
	for _, row := range store.pool {
		row := row
		srv.Handle("POST", "/instance/"+instanceID+"/vpc/ip/"+row.id+"/primary", func(w http.ResponseWriter, r *http.Request) {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.primaryCalls++
			if row.free {
				writeJSON(w, http.StatusOK, map[string]any{
					"success": false,
					"message": "VPC IP not found for this instance.",
				})
				return
			}
			for _, other := range store.pool {
				other.isPrimary = false
			}
			row.isPrimary = true
			writeJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"message": "VPC IP has been set as primary.",
			})
		})

		srv.Handle("DELETE", "/instance/"+instanceID+"/vpc/ip/"+row.id, func(w http.ResponseWriter, r *http.Request) {
			store.mu.Lock()
			defer store.mu.Unlock()
			if row.free {
				writeJSON(w, http.StatusOK, map[string]any{
					"success": false,
					"message": "VPC IP not found for this instance.",
				})
				return
			}
			if len(store.attachedRows()) <= 1 {
				writeJSON(w, http.StatusOK, map[string]any{
					"success": false,
					"message": "Cannot remove the last VPC IP. Disable VPC instead.",
				})
				return
			}
			store.removeCalls++
			wasPrimary := row.isPrimary
			row.free = true
			row.isPrimary = false
			if wasPrimary {
				if remaining := store.attachedRows(); len(remaining) > 0 {
					remaining[0].isPrimary = true
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"message": "VPC IP has been removed from this instance.",
			})
		})
	}

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + fmt.Sprintf(`
resource "iaas_instance_vpc_attachment" "test" {
  instance_id    = %q
  vpc_id         = %q
  vpc_subnet_id  = %q
  additional_ips = ["10.0.0.3"]
}
`, instanceID, vpcID, subnetID)

	updateCfg := providerCfg + fmt.Sprintf(`
resource "iaas_instance_vpc_attachment" "test" {
  instance_id    = %q
  vpc_id         = %q
  vpc_subnet_id  = %q
  additional_ips = ["10.0.0.4"]
}
`, instanceID, vpcID, subnetID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create: enable + one ip/add.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "id", instanceID),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "instance_id", instanceID),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "vpc_id", vpcID),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "vpc_subnet_id", subnetID),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "auto_assigned_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "primary_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "additional_ips.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "additional_ips.*", "10.0.0.3"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "ips.#", "2"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "ips.*", "10.0.0.2"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "ips.*", "10.0.0.3"),
				),
			},
			// 2. Import by instance_id.
			{
				ResourceName:      "iaas_instance_vpc_attachment.test",
				ImportState:       true,
				ImportStateId:     instanceID,
				ImportStateVerify: true,
			},
			// 3. Update: add 10.0.0.4, remove 10.0.0.3.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "auto_assigned_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "primary_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "additional_ips.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "additional_ips.*", "10.0.0.4"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "ips.#", "2"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "ips.*", "10.0.0.2"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_vpc_attachment.test", "ips.*", "10.0.0.4"),
				),
			},
		},
	})

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.enableCalls != 1 {
		t.Errorf("enableCalls = %d; want 1", store.enableCalls)
	}
	if store.addCalls != 2 {
		t.Errorf("addCalls = %d; want 2 (1 on create + 1 on update)", store.addCalls)
	}
	if store.removeCalls != 1 {
		t.Errorf("removeCalls = %d; want 1 (10.0.0.3 removed on update)", store.removeCalls)
	}
	if store.primaryCalls != 0 {
		t.Errorf("primaryCalls = %d; want 0 (primary_ip was never overridden away from the auto-assigned ip)", store.primaryCalls)
	}
	if store.disableCalls != 1 {
		t.Errorf("disableCalls = %d; want 1 (final destroy)", store.disableCalls)
	}

	// Assert the create's ip/add call actually carried an ip_id (never a bare
	// address - the API has no free-form ip field).
	adds := srv.Requests("POST", "/instance/"+instanceID+"/vpc/ip/add")
	if len(adds) < 1 {
		t.Fatalf("expected at least 1 POST .../vpc/ip/add call; got %d", len(adds))
	}
	for _, req := range adds {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding ip/add body: %v", err)
		}
		if id, _ := b["ip_id"].(string); id == "" || !strings.HasPrefix(id, "vip-") {
			t.Errorf("ip/add body missing/invalid ip_id: %v", b)
		}
	}
}

// ---------------------------------------------------------------------------
// TestUnitInstanceVpcAttachment_noAdditionalIPs - regression guard for the
// Optional+Computed additional_ips schema.
//
// additional_ips must be Computed (not Optional-only): Create's response sets
// it to a NON-null value (the empty set) even when the practitioner's config
// omits the attribute entirely, and terraform-plugin-testing's post-apply
// consistency check would fail a plain Optional attribute for exactly that
// mismatch (planned null → applied non-null). This test omits additional_ips
// and primary_ip altogether and simply asserts apply succeeds with only the
// server auto-assigned ip attached.
// ---------------------------------------------------------------------------
func TestUnitInstanceVpcAttachment_noAdditionalIPs(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		instanceID = "88888888-8888-8888-8888-888888888888"
		vpcID      = "99999999-9999-9999-9999-999999999999"
		subnetID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)

	store := newInstanceVpcMockServer(instanceID, vpcID, subnetID)

	srv.Handle("POST", "/instance/"+instanceID+"/vpc/enable", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.enableCalls++
		store.enabled = true
		for _, row := range store.pool {
			if row.free {
				row.free = false
				row.isPrimary = true
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC has been enabled for this instance."})
	})

	srv.Handle("POST", "/instance/"+instanceID+"/vpc/disable", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.disableCalls++
		store.enabled = false
		for _, row := range store.pool {
			row.free = true
			row.isPrimary = false
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC has been disabled for this instance."})
	})

	srv.Handle("GET", "/instance/"+instanceID+"/vpc/ips", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		rows := store.attachedRows()
		data := make([]any, 0, len(rows))
		for _, row := range rows {
			data = append(data, store.rowJSON(row))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1, "data": data, "last_page": 1, "per_page": 10, "total": len(data),
		})
	})

	config := acctest.ProviderConfig(srv.Endpoint()) + fmt.Sprintf(`
resource "iaas_instance_vpc_attachment" "test" {
  instance_id   = %q
  vpc_id        = %q
  vpc_subnet_id = %q
}
`, instanceID, vpcID, subnetID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "auto_assigned_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "primary_ip", "10.0.0.2"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "additional_ips.#", "0"),
					resource.TestCheckResourceAttr("iaas_instance_vpc_attachment.test", "ips.#", "1"),
				),
			},
		},
	})

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.disableCalls != 1 {
		t.Errorf("disableCalls = %d; want 1 (final destroy)", store.disableCalls)
	}
}
