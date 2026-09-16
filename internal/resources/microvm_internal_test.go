package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestMicrovmCreateBodyOmitsWriteOnlyMutableFields(t *testing.T) {
	plan := microvmModel{
		LocationID: types.StringValue("group-1"),
		ImageID:    types.StringValue("image-1"),
		Name:       types.StringValue("build-agent"),
		PlanID:     types.StringValue("plan-1"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"MODE": types.StringValue("test"),
		}),
		Domains: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("build.example.test")}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	if body["location_id"] != "group-1" || body["image_id"] != "image-1" || body["plan_id"] != "plan-1" {
		t.Fatalf("body = %#v", body)
	}
	for _, key := range []string{"env", "domains", "domain"} {
		if _, exists := body[key]; exists {
			t.Errorf("create body must not include separately reconciled %s", key)
		}
	}
}

// TestMicrovmCreateBodyLegacyAliasSendsCanonicalLocationID proves a plan
// configured with only the deprecated hypervisor_group_id alias still sends
// the canonical location_id key on the wire (spec 17 C4, LOC-3 pattern).
func TestMicrovmCreateBodyLegacyAliasSendsCanonicalLocationID(t *testing.T) {
	plan := microvmModel{
		HypervisorGroupID: types.StringValue("group-legacy"),
		ImageID:           types.StringValue("image-1"),
		Name:              types.StringValue("build-agent"),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	if body["location_id"] != "group-legacy" {
		t.Fatalf("body[location_id] = %v; want group-legacy", body["location_id"])
	}
	if _, exists := body["hypervisor_group_id"]; exists {
		t.Errorf("create body must never send the deprecated hypervisor_group_id key")
	}
}

// TestMicrovmCreateBodySendsSshKeyIDsAndStaticIpID proves ssh_key_ids and a
// public network entry's static_ip_id (C3/C8.3) reach the create body.
func TestMicrovmCreateBodySendsSshKeyIDsAndStaticIpID(t *testing.T) {
	networkType := types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind":               types.StringType,
		"subnet_id":          types.StringType,
		"vpc_subnet_id":      types.StringType,
		"static_ip_id":       types.StringType,
		"security_group_ids": types.ListType{ElemType: types.StringType},
		"rate_mbit":          types.Int64Type,
	}}
	entry, diags := types.ObjectValue(networkType.AttrTypes, map[string]attr.Value{
		"kind":               types.StringValue("public"),
		"subnet_id":          types.StringNull(),
		"vpc_subnet_id":      types.StringNull(),
		"static_ip_id":       types.StringValue("static-ip-1"),
		"security_group_ids": types.ListNull(types.StringType),
		"rate_mbit":          types.Int64Null(),
	})
	if diags.HasError() {
		t.Fatalf("building network entry: %v", diags)
	}
	network, diags := types.ListValue(networkType, []attr.Value{entry})
	if diags.HasError() {
		t.Fatalf("building network list: %v", diags)
	}
	plan := microvmModel{
		LocationID: types.StringValue("group-1"),
		ImageID:    types.StringValue("image-1"),
		Name:       types.StringValue("build-agent"),
		SshKeyIDs:  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("key-1"), types.StringValue("key-2")}),
		Network:    network,
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	ids, ok := body["ssh_key_ids"].([]string)
	if !ok || len(ids) != 2 || ids[0] != "key-1" || ids[1] != "key-2" {
		t.Fatalf("ssh_key_ids = %#v", body["ssh_key_ids"])
	}
	networkBody, ok := body["network"].([]map[string]any)
	if !ok || len(networkBody) != 1 {
		t.Fatalf("network = %#v", body["network"])
	}
	if networkBody[0]["static_ip_id"] != "static-ip-1" {
		t.Fatalf("network[0][static_ip_id] = %v; want static-ip-1", networkBody[0]["static_ip_id"])
	}
}

func TestMicrovmDomainReconciliationUsesDomainIDsForDelete(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"success":true,"domain":{"id":"domain-2","hostname":"new.example.test"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	res := &microvmResource{client: client.New(server.URL+"/api", "token", 0, false)}
	desired := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("new.example.test")})
	current := []map[string]any{{"id": "domain-1", "hostname": "old.example.test"}}
	if err := res.reconcileDomains(context.Background(), "vm-1", desired, current); err != nil {
		t.Fatalf("reconcileDomains: %v", err)
	}
	want := map[string]bool{
		"DELETE /api/microvm/vm/vm-1/domain/domain-1": true,
		"POST /api/microvm/vm/vm-1/domain":            true,
	}
	if len(requests) != 2 || !want[requests[0]] || !want[requests[1]] {
		t.Fatalf("requests = %v", requests)
	}
}

func TestMicrovmSchemaProtectsEnvironment(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if !resp.Schema.Attributes["env"].IsSensitive() {
		t.Fatal("env must be sensitive")
	}
	if resp.Schema.Attributes["domains"].IsSensitive() {
		t.Fatal("domains must not be sensitive")
	}
}

// TestMicrovmSchemaLocationIDIsCanonicalAndOptionalComputed pins the LOC-3
// shape: both location_id and the deprecated hypervisor_group_id are
// Optional+Computed (never Required - the API accepts either), and
// hypervisor_group_id carries a DeprecationMessage.
func TestMicrovmSchemaLocationIDIsCanonicalAndOptionalComputed(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	locationID := resp.Schema.Attributes["location_id"]
	if locationID == nil || locationID.IsRequired() || !locationID.IsOptional() || !locationID.IsComputed() {
		t.Fatalf("location_id must be Optional+Computed, never Required: %#v", locationID)
	}
	legacy := resp.Schema.Attributes["hypervisor_group_id"]
	if legacy == nil || legacy.IsRequired() || !legacy.IsOptional() || !legacy.IsComputed() {
		t.Fatalf("hypervisor_group_id must be Optional+Computed, never Required: %#v", legacy)
	}
	if legacy.GetDeprecationMessage() == "" {
		t.Fatal("hypervisor_group_id must carry a DeprecationMessage")
	}
}

// TestMicrovmSchemaNetworkKindRejectsIsolated pins C1: isolated is not a
// valid network kind - the schema validator must only accept public and vpc,
// and the schema must expose static_ip_id (C8.3).
func TestMicrovmSchemaNetworkKindRejectsIsolated(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	network, ok := resp.Schema.Attributes["network"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("network attribute is not a ListNestedAttribute: %T", resp.Schema.Attributes["network"])
	}
	kind, ok := network.NestedObject.Attributes["kind"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("network.kind is not a StringAttribute: %T", network.NestedObject.Attributes["kind"])
	}
	req := validator.StringRequest{ConfigValue: types.StringValue("isolated")}
	var validateResp validator.StringResponse
	for _, v := range kind.Validators {
		v.ValidateString(context.Background(), req, &validateResp)
	}
	if !validateResp.Diagnostics.HasError() {
		t.Fatal("network.kind must reject isolated (C1: isolated is not a valid network kind)")
	}
	if _, ok := network.NestedObject.Attributes["static_ip_id"]; !ok {
		t.Fatal("network must expose static_ip_id (C8.3)")
	}
	if subnetID, ok := network.NestedObject.Attributes["subnet_id"].(schema.StringAttribute); !ok || subnetID.IsRequired() {
		t.Fatal("network.subnet_id must stay optional (C8.1: the platform auto-assigns a public subnet)")
	}
}
