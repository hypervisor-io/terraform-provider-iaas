package resources_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// TestUnitMicrovmSettings_PublicDefault_lifecycle proves a plain `{kind =
// "public"}` default (C8.1/C8.6: no subnet_id needed) plans, applies and
// imports cleanly against the mock server.
func TestUnitMicrovmSettings_PublicDefault_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	current := map[string]any{"kind": "public"}
	srv.Handle("GET", "/microvm/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"default_network": current})
	})
	srv.Handle("PUT", "/microvm/settings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		current, _ = body["default_network"].(map[string]any)
		writeJSON(w, http.StatusOK, map[string]any{"default_network": current})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_microvm_settings" "test" {
  default_network = {
    kind = "public"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_microvm_settings.test", "id", "default"),
					resource.TestCheckResourceAttr("iaas_microvm_settings.test", "default_network.kind", "public"),
					resource.TestCheckNoResourceAttr("iaas_microvm_settings.test", "default_network.subnet_id"),
				),
			},
			{
				ResourceName:      "iaas_microvm_settings.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})

	puts := srv.Requests("PUT", "/microvm/settings")
	if len(puts) == 0 {
		t.Fatal("expected at least one PUT /microvm/settings")
	}
	var putBody map[string]any
	if err := json.Unmarshal(puts[0].Body, &putBody); err != nil {
		t.Fatalf("decoding PUT body: %v", err)
	}
	sent, ok := putBody["default_network"].(map[string]any)
	if !ok || sent["kind"] != "public" {
		t.Fatalf("PUT body default_network = %#v", putBody["default_network"])
	}
	if _, exists := sent["subnet_id"]; exists {
		t.Error("PUT body must not invent a subnet_id for a public default (C8.1)")
	}
}

// TestUnitMicrovmSettings_VpcDefault_requiresVpcSubnetID proves a vpc
// default sends vpc_subnet_id and the update path (changing an existing
// default) issues another PUT.
func TestUnitMicrovmSettings_VpcDefault_requiresVpcSubnetID(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)

	const vpcSubnetID = "55555555-5555-5555-5555-555555555555"
	current := map[string]any{}
	srv.Handle("GET", "/microvm/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"default_network": nilIfEmpty(current)})
	})
	srv.Handle("PUT", "/microvm/settings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		current, _ = body["default_network"].(map[string]any)
		writeJSON(w, http.StatusOK, map[string]any{"default_network": nilIfEmpty(current)})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_microvm_settings" "test" {
  default_network = {
    kind          = "vpc"
    vpc_subnet_id = "` + vpcSubnetID + `"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_microvm_settings.test", "default_network.kind", "vpc"),
					resource.TestCheckResourceAttr("iaas_microvm_settings.test", "default_network.vpc_subnet_id", vpcSubnetID),
				),
			},
		},
	})

	// puts[0] is the resource's own create PUT; resource.UnitTest destroys
	// the resource at test end, so a LATER entry is the Delete()-issued
	// {"default_network":null} clearing PUT, not this create's body.
	puts := srv.Requests("PUT", "/microvm/settings")
	if len(puts) == 0 {
		t.Fatal("expected at least one PUT /microvm/settings")
	}
	var putBody map[string]any
	if err := json.Unmarshal(puts[0].Body, &putBody); err != nil {
		t.Fatalf("decoding PUT body: %v", err)
	}
	sent, ok := putBody["default_network"].(map[string]any)
	if !ok || sent["kind"] != "vpc" || sent["vpc_subnet_id"] != vpcSubnetID {
		t.Fatalf("PUT body default_network = %#v", putBody["default_network"])
	}
}

func nilIfEmpty(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}
