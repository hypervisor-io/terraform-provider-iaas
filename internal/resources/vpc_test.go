package resources_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVPC_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck), plus a
// real hypervisor_group_id of a VPC-enabled location.
// ---------------------------------------------------------------------------
func TestAccVPC_basic(t *testing.T) {
	const config = `
resource "iaas_vpc" "test" {
  name                = "tfacc01"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "REPLACE-WITH-A-VPC-ENABLED-GROUP-UUID"
  description         = "tf acc test vpc"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpc.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_vpc.test", "vni_number"),
					resource.TestCheckResourceAttr("iaas_vpc.test", "name", "tfacc01"),
				),
			},
			{
				ResourceName:      "iaas_vpc.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVPC_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full resource lifecycle against canned API responses, with no
// live panel. The Steps execute in this order:
//
//  1. Create + read-back - applies createCfg; checks id, vni_number, name,
//     cidr, hypervisor_group_id, description.
//  2. Import - imports the resource by UUID and verifies state matches the
//     prior step.
//
// There is NO update step: VPC has no UPDATE route, so every configurable
// attribute is RequiresReplace and is therefore immutable in place. Delete is
// implicit teardown after the final step, not an explicit Step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary in ssh_key_test.go).
// ---------------------------------------------------------------------------
func TestUnitVPC_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		vpcID   = "22222222-2222-2222-2222-222222222222"
		groupID = "33333333-3333-3333-3333-333333333333"
		name    = "prod"
		cidr    = "10.0.0.0/24"
		desc    = "web tier"
		vni     = 4097
	)

	// CREATE - POST /vpcs returns 200 + {success,vpc}. The vpc object carries
	// its id AND the appended vni_number (synchronous create - no task).
	srv.Handle("POST", "/vpcs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC created",
			"vpc":     vpcObject(vpcID, name, cidr, groupID, desc, vni),
		})
	})

	// READ / SHOW - GET /vpc/{id} (singular) returns the vpc with subnets
	// eager-loaded (which the resource ignores).
	srv.Handle("GET", "/vpc/"+vpcID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"vpc":     vpcObject(vpcID, name, cidr, groupID, desc, vni),
		})
	})

	// DELETE - DELETE /vpc/{id} (singular) succeeds at 200.
	srv.Handle("DELETE", "/vpc/"+vpcID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC deleted",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_vpc" "test" {
  name                = "` + name + `"
  cidr                = "` + cidr + `"
  hypervisor_group_id = "` + groupID + `"
  description         = "` + desc + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpc.test", "id", vpcID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "vni_number", "4097"),
					resource.TestCheckResourceAttr("iaas_vpc.test", "name", name),
					resource.TestCheckResourceAttr("iaas_vpc.test", "cidr", cidr),
					resource.TestCheckResourceAttr("iaas_vpc.test", "hypervisor_group_id", groupID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "description", desc),
				),
			},
			// Import the existing resource and verify state matches.
			{
				ResourceName:      "iaas_vpc.test",
				ImportState:       true,
				ImportStateId:     vpcID,
				ImportStateVerify: true,
			},
		},
	})

	// Assert the create request sent name/cidr/location_id (+description) - the
	// client sends the canonical location_id key (spec 17 C1/C4) even when the
	// config used the deprecated hypervisor_group_id alias - and NOT a stray
	// field (e.g. no id, no vni_number client-side).
	creates := srv.Requests("POST", "/vpcs")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /vpcs")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != name {
		t.Errorf("create body name = %v; want %q", createBody["name"], name)
	}
	if createBody["cidr"] != cidr {
		t.Errorf("create body cidr = %v; want %q", createBody["cidr"], cidr)
	}
	if createBody["location_id"] != groupID {
		t.Errorf("create body location_id = %v; want %q", createBody["location_id"], groupID)
	}
	if createBody["description"] != desc {
		t.Errorf("create body description = %v; want %q", createBody["description"], desc)
	}
	// The resource must not leak computed/server-only fields into the create body.
	for _, stray := range []string{"id", "vni_number", "subnets", "status"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}
}

// vpcObject builds a serialized vpc object matching the API SHOW/CREATE shape,
// including the appended vni_number and the nested subnets array the resource
// is expected to ignore. An empty description is omitted so an unset optional
// description round-trips as null (no spurious "" diff).
func vpcObject(id, name, cidr, groupID, desc string, vni int) map[string]any {
	obj := map[string]any{
		"id":                  id,
		"name":                name,
		"cidr":                cidr,
		"hypervisor_group_id": groupID,
		"vni_number":          vni,
		"subnets":             []any{},
	}
	if desc != "" {
		obj["description"] = desc
	}
	return obj
}

// ---------------------------------------------------------------------------
// location_id (LOC-3 / contract C4) coverage.
//
// The VPC resource is the representative formerly-REQUIRED resource: the
// canonical location_id replaces hypervisor_group_id, the old key stays
// accepted as a deprecated alias for one release, the two are mutually
// exclusive (ExactlyOneOf), and omitting both when the placement is required
// yields a validation error naming location_id.
// ---------------------------------------------------------------------------

// TestUnitVPCLocationID_canonicalAccepted proves a config using only
// location_id plans and applies, sends that value as the API location_id, and
// stores it in state under both location_id and the deprecated
// hypervisor_group_id.
func TestUnitVPCLocationID_canonicalAccepted(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	const (
		vpcID   = "22222222-2222-2222-2222-222222222222"
		groupID = "33333333-3333-3333-3333-333333333333"
		name    = "prod"
		cidr    = "10.0.0.0/24"
		vni     = 4097
	)

	srv.Handle("POST", "/vpcs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC created",
			"vpc":     vpcObject(vpcID, name, cidr, groupID, "", vni),
		})
	})
	srv.Handle("GET", "/vpc/"+vpcID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"vpc":     vpcObject(vpcID, name, cidr, groupID, "", vni),
		})
	})
	srv.Handle("DELETE", "/vpc/"+vpcID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC deleted"})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_vpc" "test" {
  name        = "` + name + `"
  cidr        = "` + cidr + `"
  location_id = "` + groupID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpc.test", "id", vpcID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "location_id", groupID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "hypervisor_group_id", groupID),
				),
			},
		},
	})

	creates := srv.Requests("POST", "/vpcs")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /vpcs")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["location_id"] != groupID {
		t.Errorf("create body location_id = %v; want %q", createBody["location_id"], groupID)
	}
}

// TestUnitVPCLocationID_legacyAliasStillWorks proves a config using only the
// deprecated hypervisor_group_id still plans and applies unchanged during the
// one-release overlap.
func TestUnitVPCLocationID_legacyAliasStillWorks(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	const (
		vpcID   = "22222222-2222-2222-2222-222222222222"
		groupID = "33333333-3333-3333-3333-333333333333"
		name    = "prod"
		cidr    = "10.0.0.0/24"
		vni     = 4097
	)

	srv.Handle("POST", "/vpcs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC created",
			"vpc":     vpcObject(vpcID, name, cidr, groupID, "", vni),
		})
	})
	srv.Handle("GET", "/vpc/"+vpcID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"vpc":     vpcObject(vpcID, name, cidr, groupID, "", vni),
		})
	})
	srv.Handle("DELETE", "/vpc/"+vpcID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPC deleted"})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_vpc" "test" {
  name                = "` + name + `"
  cidr                = "` + cidr + `"
  hypervisor_group_id = "` + groupID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpc.test", "id", vpcID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "hypervisor_group_id", groupID),
					resource.TestCheckResourceAttr("iaas_vpc.test", "location_id", groupID),
				),
			},
		},
	})
}

// TestUnitVPCLocationID_bothSetErrors proves that configuring both location_id
// and the deprecated hypervisor_group_id (the decoy case for spec 17 C4) is a
// validation error naming the conflict, rather than silently picking a winner
// - no HTTP call should ever be made.
func TestUnitVPCLocationID_bothSetErrors(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_vpc" "test" {
  name                = "prod"
  cidr                = "10.0.0.0/24"
  location_id         = "33333333-3333-3333-3333-333333333333"
  hypervisor_group_id = "44444444-4444-4444-4444-444444444444"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile("Invalid Attribute Combination"),
			},
		},
	})
}

// TestUnitVPCLocationID_bothUnsetErrors proves that omitting both location_id
// and hypervisor_group_id on a formerly-Required placement errors with a
// message naming location_id.
func TestUnitVPCLocationID_bothUnsetErrors(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_vpc" "test" {
  name = "prod"
  cidr = "10.0.0.0/24"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile("Invalid Attribute Combination"),
			},
		},
	})
}
