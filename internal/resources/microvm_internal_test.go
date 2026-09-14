package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestMicrovmCreateBodyOmitsWriteOnlyMutableFields(t *testing.T) {
	plan := microvmModel{
		HypervisorGroupID: types.StringValue("group-1"),
		ImageID:           types.StringValue("image-1"),
		Name:              types.StringValue("build-agent"),
		PlanID:            types.StringValue("plan-1"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"MODE": types.StringValue("test"),
		}),
		Domains: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("build.example.test")}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	if body["hypervisor_group_id"] != "group-1" || body["image_id"] != "image-1" || body["plan_id"] != "plan-1" {
		t.Fatalf("body = %#v", body)
	}
	for _, key := range []string{"env", "domains", "domain"} {
		if _, exists := body[key]; exists {
			t.Errorf("create body must not include separately reconciled %s", key)
		}
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
