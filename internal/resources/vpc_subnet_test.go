package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVPCSubnet_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck), plus a real
// hypervisor_group_id of a VPC-enabled location. A parent iaas_vpc is created in
// the same config so vpc_id references it.
//
// The import step uses ImportStateIdFunc to build the COMPOSITE import id
// "<vpc_id>/<subnet_id>" from the live state, since a child resource cannot be
// imported by its subnet id alone.
// ---------------------------------------------------------------------------
func TestAccVPCSubnet_basic(t *testing.T) {
	const config = `
resource "iaas_vpc" "test" {
  name                = "tfacc02"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "REPLACE-WITH-A-VPC-ENABLED-GROUP-UUID"
}

resource "iaas_vpc_subnet" "test" {
  vpc_id = iaas_vpc.test.id
  cidr   = "192.168.50.0/24"
  name   = "tf acc subnet"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpc_subnet.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_vpc_subnet.test", "netmask"),
					resource.TestCheckResourceAttrSet("iaas_vpc_subnet.test", "gateway"),
					resource.TestCheckResourceAttrPair("iaas_vpc_subnet.test", "vpc_id", "iaas_vpc.test", "id"),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "cidr", "192.168.50.0/24"),
				),
			},
			{
				ResourceName:      "iaas_vpc_subnet.test",
				ImportState:       true,
				ImportStateIdFunc: vpcSubnetImportStateIDFunc("iaas_vpc_subnet.test"),
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVPCSubnet_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full child-resource lifecycle against canned API responses, with
// no live panel. The Steps execute in this order:
//
//  1. Create + read-back - applies createCfg; checks id, netmask, gateway, used,
//     free, used_percentage, name (parent vpc created alongside).
//  2. Import - imports via the COMPOSITE id "<vpc_id>/<subnet_id>" built by
//     ImportStateIdFunc; ImportStateVerify: true.
//  3. Update - applies updateCfg (renamed name); checks new name.
//
// Delete is implicit teardown after the final step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary in ssh_key_test.go).
// ---------------------------------------------------------------------------
func TestUnitVPCSubnet_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		// Parent VPC.
		vpcID   = "44444444-4444-4444-4444-444444444444"
		groupID = "55555555-5555-5555-5555-555555555555"
		vpcName = "childhost"
		vpcCidr = "10.0.0.0/24"
		vni     = 5000

		// Subnet (child).
		subnetID = "66666666-6666-6666-6666-666666666666"
		cidr     = "192.168.10.0/24"
		netmask  = "255.255.255.0"
		gateway  = "192.168.10.1"
		subType  = "public"
		// Stable used/free across the lifecycle so ImportStateVerify passes even
		// though these server-mutable computed fields omit UseStateForUnknown.
		used      = 0
		free      = 253
		usedPct   = 0.0
		createNam = "lifecycle subnet"
		updateNam = "renamed subnet"
	)

	// currentName tracks the server-side subnet name so READ/IMPORT reflect the
	// latest value set by create/update - exercising real drift-free read-back.
	currentName := createNam

	// --- Parent VPC handlers (so vpc_id can reference a real iaas_vpc) ---
	// The parent VPC config omits description, so the mock must NOT return a
	// description key - otherwise the vpc resource maps "" over a null optional
	// and the framework reports an inconsistent-result error. Deleting the key
	// lets optionalStringFromAPI keep description null (round-trips cleanly).
	parentVPC := func() map[string]any {
		obj := vpcObject(vpcID, vpcName, vpcCidr, groupID, "", vni)
		delete(obj, "description")
		return obj
	}
	srv.Handle("POST", "/vpcs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"vpc":     parentVPC(),
		})
	})
	srv.Handle("GET", "/vpc/"+vpcID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"vpc":     parentVPC(),
		})
	})
	srv.Handle("DELETE", "/vpc/"+vpcID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC deleted"})
	})

	// --- Subnet (child) handlers ---
	// CREATE - POST /vpc/{vpcId}/subnets (PLURAL). Returns the row with id +
	// server-derived gateway/netmask.
	srv.Handle("POST", "/vpc/"+vpcID+"/subnets", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Subnet created",
			"subnet":  subnetObject(subnetID, cidr, netmask, gateway, subType, currentName, used, free, usedPct),
		})
	})
	// SHOW - GET /vpc/{vpcId}/subnet/{id} (SINGULAR). Reflects the latest name;
	// used/free are STABLE so ImportStateVerify passes.
	srv.Handle("GET", "/vpc/"+vpcID+"/subnet/"+subnetID, func(w http.ResponseWriter, r *http.Request) {
		obj := subnetObject(subnetID, cidr, netmask, gateway, subType, currentName, used, free, usedPct)
		obj["ips"] = []any{} // SHOW eager-loads ips; the resource ignores them.
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"subnet":  obj,
		})
	})
	// UPDATE - PATCH /vpc/{vpcId}/subnet/{id} (SINGULAR). Applies the new name.
	srv.Handle("PATCH", "/vpc/"+vpcID+"/subnet/"+subnetID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Subnet updated",
			"subnet":  subnetObject(subnetID, cidr, netmask, gateway, subType, currentName, used, free, usedPct),
		})
	})
	// DELETE - DELETE /vpc/{vpcId}/subnet/{id} (SINGULAR).
	srv.Handle("DELETE", "/vpc/"+vpcID+"/subnet/"+subnetID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Subnet deleted"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	vpcBlock := `
resource "iaas_vpc" "parent" {
  name                = "` + vpcName + `"
  cidr                = "` + vpcCidr + `"
  hypervisor_group_id = "` + groupID + `"
}
`
	createCfg := providerCfg + vpcBlock + `
resource "iaas_vpc_subnet" "test" {
  vpc_id = iaas_vpc.parent.id
  cidr   = "` + cidr + `"
  name   = "` + createNam + `"
}
`
	updateCfg := providerCfg + vpcBlock + `
resource "iaas_vpc_subnet" "test" {
  vpc_id = iaas_vpc.parent.id
  cidr   = "` + cidr + `"
  name   = "` + updateNam + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "id", subnetID),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "vpc_id", vpcID),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "cidr", cidr),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "netmask", netmask),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "gateway", gateway),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "type", subType),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "name", createNam),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "used", "0"),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "free", "253"),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "used_percentage", "0"),
				),
			},
			// Import via the COMPOSITE id "<vpc_id>/<subnet_id>".
			{
				ResourceName:      "iaas_vpc_subnet.test",
				ImportState:       true,
				ImportStateIdFunc: vpcSubnetImportStateIDFunc("iaas_vpc_subnet.test"),
				ImportStateVerify: true,
			},
			// Update the name (the only in-place mutable field).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "id", subnetID),
					resource.TestCheckResourceAttr("iaas_vpc_subnet.test", "name", updateNam),
				),
			},
		},
	})

	// Assert the create request hit the PLURAL collection path and sent cidr but
	// NOT the server-derived gateway/netmask (nor id).
	creates := srv.Requests("POST", "/vpc/"+vpcID+"/subnets")
	if len(creates) == 0 {
		t.Fatalf("expected at least one POST /vpc/%s/subnets", vpcID)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["cidr"] != cidr {
		t.Errorf("create body cidr = %v; want %q", createBody["cidr"], cidr)
	}
	if createBody["name"] != createNam {
		t.Errorf("create body name = %v; want %q", createBody["name"], createNam)
	}
	for _, stray := range []string{"gateway", "netmask", "id", "used", "free", "used_percentage", "vpc_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q (server-derived/server-only); got %v", stray, createBody)
		}
	}
}

// vpcSubnetImportStateIDFunc returns an ImportStateIdFunc that builds the
// COMPOSITE import id "<vpc_id>/<subnet_id>" from the resource's state. A child
// resource cannot be imported by its own id alone - the parent vpc_id is needed
// to build the API path - so the import id joins both with a slash. This mirrors
// the split performed by the resource's ImportState.
func vpcSubnetImportStateIDFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		vpcID := rs.Primary.Attributes["vpc_id"]
		id := rs.Primary.Attributes["id"]
		if vpcID == "" || id == "" {
			return "", fmt.Errorf("resource %s missing vpc_id or id in state", resourceName)
		}
		return vpcID + "/" + id, nil
	}
}

// subnetObject builds a serialized subnet object matching the API SHOW/CREATE
// shape (id, cidr, derived netmask/gateway, type, name, used/free, and the
// appended used_percentage). The vpc_id is intentionally NOT included - it lives
// in the URL path, not the body - matching the real controller.
func subnetObject(id, cidr, netmask, gateway, subType, name string, used, free int, usedPct float64) map[string]any {
	return map[string]any{
		"id":              id,
		"cidr":            cidr,
		"netmask":         netmask,
		"gateway":         gateway,
		"type":            subType,
		"name":            name,
		"used":            used,
		"free":            free,
		"used_percentage": usedPct,
	}
}
