package resources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// vpcDeleteServer answers each DELETE /api/vpc/{id} with the next canned
// response (the last one repeats) and counts the requests.
func vpcDeleteServer(t *testing.T, calls *int32, responses ...func(w http.ResponseWriter)) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/vpc/v1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		n := int(atomic.AddInt32(calls, 1)) - 1
		if n >= len(responses) {
			n = len(responses) - 1
		}
		responses[n](w)
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL+"/api", "tok", 10*time.Second, false)
}

func respond(status int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const (
	pendingBody    = `{"success":false,"reason":"vpc_delete_pending","message":"A NAT gateway is still being removed","pending_nat_gateways":["gw-1","gw-2"]}`
	incompleteBody = `{"success":false,"reason":"vpc_delete_incomplete","message":"A later step failed"}`
	okBody         = `{"success":true,"message":"VPC deleted"}`
)

func TestVPCDelete_pendingThenGone(t *testing.T) {
	var calls int32
	c := vpcDeleteServer(t, &calls, respond(409, pendingBody), respond(409, pendingBody), respond(200, okBody))
	if err := deleteVPCUntilGone(context.Background(), c, "v1", 5*time.Second, time.Millisecond); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Errorf("DELETE sent %d times; want 3", calls)
	}
}

func TestVPCDelete_incompleteThenGone(t *testing.T) {
	var calls int32
	c := vpcDeleteServer(t, &calls, respond(409, incompleteBody), respond(404, `{"message":"Not Found"}`))
	if err := deleteVPCUntilGone(context.Background(), c, "v1", 5*time.Second, time.Millisecond); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 2 {
		t.Errorf("DELETE sent %d times; want 2", calls)
	}
}

func TestVPCDelete_timeoutNamesPendingGateways(t *testing.T) {
	var calls int32
	c := vpcDeleteServer(t, &calls, respond(409, pendingBody))
	err := deleteVPCUntilGone(context.Background(), c, "v1", 100*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	for _, want := range []string{"gw-1", "gw-2", "vpc_delete_pending", "run destroy again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if calls < 2 {
		t.Errorf("DELETE sent %d times; want a repeat", calls)
	}
}

func TestVPCDelete_otherRefusalIsNotRetried(t *testing.T) {
	var calls int32
	c := vpcDeleteServer(t, &calls, respond(200, `{"success":false,"message":"Subnet still has used IPs"}`))
	err := deleteVPCUntilGone(context.Background(), c, "v1", 5*time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Subnet still has used IPs") {
		t.Fatalf("expected the refusal, got %v", err)
	}
	if calls != 1 {
		t.Errorf("DELETE sent %d times; want 1", calls)
	}

	calls = 0
	c = vpcDeleteServer(t, &calls, respond(409, `{"success":false,"reason":"vpc_in_use","message":"busy"}`))
	err = deleteVPCUntilGone(context.Background(), c, "v1", 5*time.Second, time.Millisecond)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "vpc_in_use" || calls != 1 {
		t.Fatalf("a different 409 reason must end the wait at once; err=%v calls=%d", err, calls)
	}
}

func TestVPCCreate_vpcDeletingDiagnostic(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		respond(409, `{"success":false,"reason":"vpc_deleting","message":"The VPC is being deleted"}`)(w)
	}))
	defer srv.Close()
	c := client.New(srv.URL+"/api", "tok", 10*time.Second, false)

	_, err := c.CreateNatGateway(context.Background(), "v1", map[string]any{"name": "gw"})
	if err == nil {
		t.Fatal("expected an error")
	}
	d := tfdiag.FromErr("Error creating NAT gateway", err)
	for _, want := range []string{"being deleted", "not retried", "reason: vpc_deleting"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("diagnostic %q does not mention %q", d.Detail(), want)
		}
	}
	if calls != 1 {
		t.Errorf("create sent %d times; a vpc_deleting refusal must not be retried", calls)
	}
}
