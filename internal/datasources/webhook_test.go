package datasources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestWebhookEventKindsToListMapsEveryRow(t *testing.T) {
	rows := []map[string]any{
		{"kind": "cluster.created", "resource": "kubernetes_cluster", "description": "A Kubernetes cluster finished provisioning."},
		{"kind": "instance.created", "resource": "instance", "description": "An instance was created."},
	}
	list, diags := webhookEventKindsToList(rows)
	if diags.HasError() {
		t.Fatalf("webhookEventKindsToList: %v", diags)
	}
	if len(list.Elements()) != 2 {
		t.Fatalf("kinds = %v; want two", list)
	}
	first := list.Elements()[0].(types.Object)
	if first.Attributes()["kind"].(types.String).ValueString() != "cluster.created" {
		t.Fatalf("kind = %v", first)
	}
	if first.Attributes()["resource"].(types.String).ValueString() != "kubernetes_cluster" {
		t.Fatalf("resource = %v", first)
	}
	second := list.Elements()[1].(types.Object)
	if second.Attributes()["kind"].(types.String).ValueString() != "instance.created" {
		t.Fatalf("kind = %v", second)
	}
}

func TestWebhookEventKindsToListEmpty(t *testing.T) {
	list, diags := webhookEventKindsToList(nil)
	if diags.HasError() {
		t.Fatalf("webhookEventKindsToList: %v", diags)
	}
	if len(list.Elements()) != 0 {
		t.Fatalf("kinds = %v; want empty", list)
	}
}
