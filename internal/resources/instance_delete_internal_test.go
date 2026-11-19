package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// The panel's instance delete is asynchronous and bounded: a 200 means the host
// accepted it, a repeat while it runs answers 409 delete_in_progress, and an
// episode the panel gave up on shows delete_state "failed" with delete_error on
// the instance (which is still there). These tests drive the real Delete().

type instDelFake struct {
	deletes   int32
	shows     int32
	onDelete  func(n int32, w http.ResponseWriter)
	showReply func(n int32) (int, string)
}

func instDelFixture(t *testing.T, f *instDelFake) (*instanceResource, resource.DeleteRequest) {
	t.Helper()
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "2ms")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/cloud-service/instances/i1"):
			f.onDelete(atomic.AddInt32(&f.deletes, 1), w)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/instance/i1"):
			code, body := f.showReply(atomic.AddInt32(&f.shows, 1))
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	r := &instanceResource{client: client.New(srv.URL+"/api", "tok", 5*time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "i1")
	return r, resource.DeleteRequest{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}}
}

func instDelOK(_ int32, w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success":true,"message":"Instance deletion queued"}`))
}

const instRunning = `{"id":"i1","status":1,"delete_state":null}`
const instDeleting = `{"id":"i1","status":1,"delete_state":"deleting"}`

func TestInstanceDelete_AcceptedThenConfirmed(t *testing.T) {
	f := &instDelFake{onDelete: instDelOK, showReply: func(n int32) (int, string) {
		switch n {
		case 1:
			return 200, instRunning
		case 2:
			return 200, instDeleting
		}
		return 404, `{"message":"Instance not found"}`
	}}
	r, req := instDelFixture(t, f)
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if f.deletes != 1 || f.shows < 3 {
		t.Fatalf("deletes=%d shows=%d; want one delete and a poll until 404", f.deletes, f.shows)
	}
}

func TestInstanceDelete_409DeleteInProgressIsAcceptedAndPolled(t *testing.T) {
	f := &instDelFake{onDelete: func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"delete_in_progress","message":"This instance is already being deleted."}`))
	}, showReply: func(n int32) (int, string) {
		if n < 3 {
			return 200, instDeleting
		}
		return 404, `{}`
	}}
	r, req := instDelFixture(t, f)
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("409 delete_in_progress must be accepted: %v", resp.Diagnostics)
	}
	if f.deletes != 1 || f.shows < 3 {
		t.Fatalf("deletes=%d shows=%d; the 409 must be sent once and polled to 404", f.deletes, f.shows)
	}
}

func TestInstanceDelete_FailedStateStopsAtOnceWithDeleteError(t *testing.T) {
	f := &instDelFake{onDelete: instDelOK, showReply: func(n int32) (int, string) {
		if n < 2 {
			return 200, instDeleting
		}
		return 200, `{"id":"i1","status":1,"delete_state":"failed","delete_error":"The host did not confirm the delete after 5 attempts.","delete_reason":"node_unconfirmed","delete_attempts":5}`
	}}
	r, req := instDelFixture(t, f)
	// A long timeout: stopping at once is proven by returning long before it.
	start := time.Now()
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed delete must fail the destroy (the instance still exists)")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("did not stop at once: %v", time.Since(start))
	}
	d := resp.Diagnostics.Errors()[0]
	for _, want := range []string{"i1", "did not confirm the delete after 5 attempts", "node_unconfirmed", "5 attempts", "again"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("detail %q lacks %q", d.Detail(), want)
		}
	}
	if f.deletes != 1 {
		t.Errorf("deletes=%d; the provider must not re-send a failed delete itself", f.deletes)
	}
}

func TestInstanceDelete_PublicIPInFlightIsRepeatedAfterTheNamedWait(t *testing.T) {
	var waits []time.Duration
	instanceDeleteRetryWait = func(d time.Duration) time.Duration { waits = append(waits, d); return time.Millisecond }
	t.Cleanup(func() { instanceDeleteRetryWait = nil })
	f := &instDelFake{onDelete: func(n int32, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"public_ip_operation_in_flight","message":"A public IP change is in flight."}`))
			return
		}
		instDelOK(n, w)
	}, showReply: func(int32) (int, string) { return 404, `{}` }}
	r, req := instDelFixture(t, f)
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() || f.deletes != 2 {
		t.Fatalf("diags=%v deletes=%d; want the delete repeated once", resp.Diagnostics, f.deletes)
	}
	if len(waits) != 1 || waits[0] != 2*time.Minute {
		t.Errorf("waits=%v; want one wait of the named 120s", waits)
	}
}

func TestInstanceDelete_OtherConflictsFailWithoutPolling(t *testing.T) {
	f := &instDelFake{onDelete: func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"instance_migrating","message":"Instance is migrating."}`))
	}, showReply: func(int32) (int, string) { return 200, instDeleting }}
	r, req := instDelFixture(t, f)
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() || f.shows != 0 || f.deletes != 1 {
		t.Fatalf("diags=%v deletes=%d shows=%d; another 409 is an error, never polled or repeated", resp.Diagnostics, f.deletes, f.shows)
	}
}
