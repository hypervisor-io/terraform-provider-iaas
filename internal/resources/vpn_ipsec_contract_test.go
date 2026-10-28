package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestVpnIpsecContract_ComputedStatusAndSecretSchema(t *testing.T) {
	r := &vpnIpsecConnectionResource{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if !resp.Schema.Attributes["psk"].IsSensitive() {
		t.Fatal("psk must remain sensitive")
	}
	for _, key := range []string{"children_expected", "children_installed", "remote_host", "established_at", "bytes_in", "bytes_out", "last_error_at", "status_checked_at", "gateway_last_error_code", "gateway_last_error_at"} {
		attr, ok := resp.Schema.Attributes[key]
		if !ok || !attr.IsComputed() || attr.IsOptional() || attr.IsRequired() {
			t.Errorf("%s must be computed only", key)
		}
	}
}

func TestVpnIpsecContract_StatusRefreshClearsStaleDiagnosis(t *testing.T) {
	prior := vpnIpsecConnectionModel{PSK: types.StringValue("configured-secret"), LastError: types.StringValue("old"), RemoteHost: types.StringValue("old-host"), BytesIn: types.Int64Value(999)}
	got := vpnIpsecStateFromAPI(map[string]any{
		"psk": "must-not-be-adopted", "children_expected": float64(2), "children_installed": float64(1),
		"remote_host": "198.51.100.7", "established_at": "2026-10-28T11:00:00+00:00",
		"bytes_in": float64(123), "bytes_out": float64(456),
		"last_error_code": "esp_proposal_mismatch", "last_error": "The peers could not agree on an ESP proposal.",
		"last_error_at": "2026-10-28T12:00:00+00:00", "status_checked_at": "2026-10-28T12:00:01+00:00",
	}, prior)
	if got.ChildrenExpected.ValueInt64() != 2 || got.ChildrenInstalled.ValueInt64() != 1 || got.BytesIn.ValueInt64() != 123 || got.BytesOut.ValueInt64() != 456 || got.RemoteHost.ValueString() != "198.51.100.7" || got.LastErrorAt.IsNull() || got.StatusCheckedAt.IsNull() || got.EstablishedAt.IsNull() {
		t.Fatal("live status fields were not mapped")
	}
	if !got.PSK.Equal(prior.PSK) {
		t.Fatal("a response changed the configured psk")
	}
	next := vpnIpsecStateFromAPI(map[string]any{"last_error": nil, "last_error_code": nil, "last_error_at": nil, "remote_host": nil, "established_at": nil, "children_installed": float64(0), "bytes_in": float64(0), "bytes_out": float64(0)}, got)
	if !next.LastError.IsNull() || !next.LastErrorCode.IsNull() || !next.LastErrorAt.IsNull() || !next.RemoteHost.IsNull() || !next.EstablishedAt.IsNull() || next.BytesIn.ValueInt64() != 0 || next.ChildrenInstalled.ValueInt64() != 0 {
		t.Fatal("cleared status retained stale evidence")
	}
}

func TestVpnIpsecContract_GatewayDiagnosisClearsAndPollFailurePreserves(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/vpn-gateway/g/ipsec" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"success":true,"connections":[],"gateway":{"ipsec_last_error_code":"no_proposal_chosen","ipsec_last_error_at":"2026-10-28T12:00:00+00:00","ipsec_error":null}}`))
		case 2:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"success":false,"message":"Forbidden"}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"connections":[],"gateway":{"ipsec_last_error_code":null,"ipsec_last_error_at":null,"ipsec_error":null}}`))
		}
	}))
	defer srv.Close()
	r := &vpnIpsecConnectionResource{client: client.New(srv.URL+"/api", "tok", time.Second, false)}
	state := vpnIpsecConnectionModel{}
	var d diag.Diagnostics
	r.gatewayDiagnostics(context.Background(), "g", &state, &d)
	if state.GatewayLastErrorCode.ValueString() != "no_proposal_chosen" || state.GatewayLastErrorAt.IsNull() || len(d) != 0 {
		t.Fatal("gateway diagnosis missing")
	}
	r.gatewayDiagnostics(context.Background(), "g", &state, &d)
	if state.GatewayLastErrorCode.ValueString() != "no_proposal_chosen" || len(d) != 1 || d.HasError() {
		t.Fatal("failed poll must warn and retain last known diagnosis")
	}
	r.gatewayDiagnostics(context.Background(), "g", &state, &d)
	if !state.GatewayLastErrorCode.IsNull() || !state.GatewayLastErrorAt.IsNull() {
		t.Fatal("successful null diagnosis must clear prior error")
	}
}

func TestVpnIpsecContract_InstallationRetryRequiresConflict(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
		retry  bool
	}{
		{409, "ipsec_installing", true}, {422, "ipsec_installing", false}, {500, "ipsec_installing", false},
		{409, "remote_identity_in_use", false}, {409, "node_tls_unpinned", false}, {422, "validation_failed", false},
	} {
		err := &client.APIError{Status: tc.status, Reason: tc.reason}
		_, retry := retryWait(err)
		if retry != tc.retry {
			t.Errorf("status %d reason %s retry=%v want %v", tc.status, tc.reason, retry, tc.retry)
		}
	}
}

// Exercise the resource callback itself: a typed teardown refusal must retain
// the complete state, while a missing or successfully deleted gateway is gone.
func TestVpnGatewayContract_DeleteState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		retained bool
	}{
		{"refused", 409, `{"success":false,"reason":"vpn_gateway_destroy_failed","message":"The VPN gateway could not be deleted and has been kept; try deleting it again."}`, true},
		{"already_gone", 404, `{"success":false,"reason":"not_found","message":"VPN gateway not found."}`, false},
		{"accepted", 200, `{"success":true,"message":"VPN gateway deleted successfully"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "DELETE" || r.URL.Path != "/api/vpn-gateway/g" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			ctx := context.Background()
			r := &vpnGatewayResource{client: client.New(srv.URL+"/api", "tok", time.Second, false)}
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
			for key, value := range map[string]string{"id": "g", "vpc_id": "vpc-1", "name": "retained-gateway", "status": "active", "public_ip": "203.0.113.10"} {
				if d := state.SetAttribute(ctx, path.Root(key), types.StringValue(value)); d.HasError() {
					t.Fatalf("building %s state: %v", key, d)
				}
			}
			// The framework pre-populates DeleteResponse.State from the request.
			resp := resource.DeleteResponse{State: state}
			r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			if calls != 1 {
				t.Fatalf("gateway delete sent %d requests; want one", calls)
			}
			if tc.retained {
				if !resp.Diagnostics.HasError() {
					t.Fatal("409 refusal must return an error")
				}
				if resp.State.Raw.IsNull() || !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("409 refusal changed or removed prior Terraform state")
				}
				foundReason := false
				for _, d := range resp.Diagnostics {
					if strings.Contains(d.Detail(), client.ReasonVpnGatewayDestroyFailed) {
						foundReason = true
					}
				}
				if !foundReason {
					t.Fatal("409 diagnostic lost the typed reason")
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("successful deletion returned errors: %v", resp.Diagnostics)
				}
				if !resp.State.Raw.IsNull() {
					t.Fatal("deleted gateway remains in Terraform state")
				}
			}
		})
	}
}
