package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func metaMap(kv map[string]string) types.Map {
	values := map[string]attr.Value{}
	for k, v := range kv {
		values[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, values)
}

func TestMicrovmCreateBodySendsMetadata(t *testing.T) {
	plan := microvmModel{
		LocationID: types.StringValue("g"), ImageID: types.StringValue("i"), Name: types.StringValue("n"),
		Metadata: metaMap(map[string]string{"team": "pay"}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatal(diags)
	}
	meta, _ := body["metadata"].(map[string]string)
	if meta["team"] != "pay" {
		t.Fatalf("body[metadata] = %#v", body["metadata"])
	}

	plan.Metadata = types.MapNull(types.StringType)
	body, _ = microvmCreateBody(context.Background(), plan)
	if _, exists := body["metadata"]; exists {
		t.Fatal("an unconfigured metadata must not be sent")
	}
}

func TestMicrovmMetadataPatch(t *testing.T) {
	ctx := context.Background()
	patch, err := microvmMetadataPatch(ctx, metaMap(map[string]string{"a": "1", "b": "changed", "c": "new"}), metaMap(map[string]string{"a": "1", "b": "2", "gone": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 3 || patch["b"] != "changed" || patch["c"] != "new" {
		t.Fatalf("patch = %#v", patch)
	}
	if v, present := patch["gone"]; !present || v != nil {
		t.Fatalf("a dropped key must be a nil removal, patch = %#v", patch)
	}
	if _, present := patch["a"]; present {
		t.Fatal("an unchanged key must not be re-sent")
	}
	same, _ := microvmMetadataPatch(ctx, metaMap(map[string]string{"a": "1"}), metaMap(map[string]string{"a": "1"}))
	if same != nil {
		t.Fatalf("equal maps must give no request, got %#v", same)
	}
	cleared, _ := microvmMetadataPatch(ctx, types.MapNull(types.StringType), metaMap(map[string]string{"a": "1"}))
	if v, present := cleared["a"]; !present || v != nil {
		t.Fatalf("removing the attribute must remove its keys, got %#v", cleared)
	}
}

func TestMicrovmStateReadsMetadataOnlyWhenManaged(t *testing.T) {
	var diags diag.Diagnostics
	obj := map[string]any{"id": "vm-1", "metadata": map[string]any{"team": "pay", "nested": map[string]any{"x": 1}}}

	managed := microvmStateFromAPI(context.Background(), obj, nil, microvmModel{Metadata: metaMap(map[string]string{"team": "old"})}, &diags)
	got := map[string]string{}
	managed.Metadata.ElementsAs(context.Background(), &got, false)
	if len(got) != 1 || got["team"] != "pay" {
		t.Fatalf("managed metadata = %v (non-string values must be skipped)", got)
	}

	unmanaged := microvmStateFromAPI(context.Background(), obj, nil, microvmModel{Metadata: types.MapNull(types.StringType)}, &diags)
	if !unmanaged.Metadata.IsNull() {
		t.Fatal("metadata not named in the configuration must stay null")
	}
}

func TestMicrovmSchemaMetadataUpdatesInPlace(t *testing.T) {
	var resp resource.SchemaResponse
	(&microvmResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["metadata"]
	if !ok {
		t.Fatal("iaas_microvm has no metadata attribute")
	}
	if !attr.IsOptional() || attr.IsComputed() {
		t.Fatal("metadata must be optional, not computed")
	}
	mapAttr, ok := attr.(schema.MapAttribute)
	if !ok {
		t.Fatalf("metadata is %T, want a map attribute", attr)
	}
	if len(mapAttr.PlanModifiers) != 0 {
		t.Fatal("metadata must not carry RequiresReplace: the API patches it in place")
	}
}
