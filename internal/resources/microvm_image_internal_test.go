package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMicrovmImageCreateBodyKeepsSecretsOutOfSource(t *testing.T) {
	plan := microvmImageModel{
		Name:              types.StringValue("base-tools"),
		SourceKind:        types.StringValue("oci"),
		SourceImage:       types.StringValue("registry.example.test/base-tools:1"),
		RegistryUsername:  types.StringValue("builder"),
		RegistryPassword:  types.StringValue("fixture-password"),
		HypervisorGroupID: types.StringValue("group-1"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"MODE": types.StringValue("test"),
		}),
		LifecycleHooks: types.StringValue(`{"port":8080}`),
	}

	body, err := microvmImageCreateBody(context.Background(), plan)
	if err != nil {
		t.Fatalf("microvmImageCreateBody: %v", err)
	}
	source := body["source"].(map[string]any)
	if source["image"] != "registry.example.test/base-tools:1" {
		t.Fatalf("source = %#v", source)
	}
	if _, exists := source["password"]; exists {
		t.Fatal("registry password leaked into source")
	}
	auth := body["auth"].(map[string]any)
	if auth["password"] != "fixture-password" || body["env"] != `{"MODE":"test"}` {
		t.Fatalf("body auth/env = %#v/%#v", auth, body["env"])
	}
}

func TestMicrovmImageSchemaMarksCredentialsSensitive(t *testing.T) {
	res := NewMicrovmImageResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"registry_password", "env"} {
		attribute := resp.Schema.Attributes[name]
		if !attribute.IsSensitive() {
			t.Errorf("%s must be sensitive", name)
		}
	}
}
