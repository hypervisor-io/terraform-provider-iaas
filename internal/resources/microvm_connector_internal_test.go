package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMicrovmConnectorCreateBodiesFollowKind(t *testing.T) {
	githubPlan := microvmConnectorModel{
		Kind:        types.StringValue("github_app"),
		Name:        types.StringValue("github"),
		GitSourceID: types.StringValue("source-1"),
		Labels: types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("linux"),
		}),
	}
	body, diags := microvmConnectorCreateBody(context.Background(), githubPlan)
	if diags.HasError() || body["git_source_id"] != "source-1" {
		t.Fatalf("github body/diags = %#v/%v", body, diags)
	}
	if _, exists := body["gitlab_token"]; exists {
		t.Fatal("github body contains gitlab_token")
	}

	gitlabPlan := microvmConnectorModel{
		Kind:              types.StringValue("gitlab_runner"),
		Name:              types.StringValue("gitlab"),
		GitlabURL:         types.StringValue("https://gitlab.example.test"),
		GitlabToken:       types.StringValue("glrt-fixture"),
		GitlabRunUntagged: types.BoolValue(true),
	}
	body, diags = microvmConnectorCreateBody(context.Background(), gitlabPlan)
	if diags.HasError() || body["gitlab_token"] != "glrt-fixture" || body["gitlab_run_untagged"] != true {
		t.Fatalf("gitlab body/diags = %#v/%v", body, diags)
	}
}

func TestMicrovmConnectorReadPreservesWriteOnlyToken(t *testing.T) {
	prior := microvmConnectorModel{GitlabToken: types.StringValue("glrt-fixture")}
	var diags diag.Diagnostics
	state := microvmConnectorStateFromAPI(context.Background(), map[string]any{
		"id":      "connector-1",
		"kind":    "gitlab_runner",
		"name":    "gitlab",
		"enabled": true,
	}, prior, &diags)
	if diags.HasError() {
		t.Fatalf("state diagnostics: %v", diags)
	}
	if state.GitlabToken.ValueString() != "glrt-fixture" {
		t.Fatal("read discarded write-only gitlab token")
	}
}

func TestMicrovmConnectorSchemaMarksTokenSensitive(t *testing.T) {
	res := NewMicrovmConnectorResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if !resp.Schema.Attributes["gitlab_token"].IsSensitive() {
		t.Fatal("gitlab_token must be sensitive")
	}
}
