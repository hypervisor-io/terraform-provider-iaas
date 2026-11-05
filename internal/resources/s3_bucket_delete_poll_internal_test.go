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

// 341-S3B follow-up: the platform's bucket DELETE is asynchronous (202 "deleting"), so Delete must poll SHOW until
// 404, a delete_state "failed" (error with delete_error) or the timeout.

func deleteFixture(t *testing.T, onDelete http.HandlerFunc, show func(n int32) (int, string)) (*s3BucketResource, resource.DeleteRequest, *int32) {
	t.Helper()
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "5ms")
	var shows int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			onDelete(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/object-storage/bucket/b1"):
			code, body := show(atomic.AddInt32(&shows, 1))
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	r := &s3BucketResource{client: client.New(srv.URL+"/api", "tok", 5*time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "b1")
	return r, resource.DeleteRequest{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}}, &shows
}

func accepted(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"success":true,"status":"deleting","message":"Bucket deletion has been queued."}`))
}

const bucketDeleting = `{"bucket":{"id":"b1","delete_state":"deleting"}}`

func TestS3BucketDelete_WaitsUntilTheBucketIsGone(t *testing.T) {
	r, req, shows := deleteFixture(t, accepted, func(n int32) (int, string) {
		if n < 3 {
			return 200, bucketDeleting
		}
		return 404, `{"message":"S3 Bucket not found"}`
	})
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if atomic.LoadInt32(shows) < 3 {
		t.Errorf("Delete returned after %d SHOW calls; it must poll until 404", *shows)
	}
}

func TestS3BucketDelete_FailedDeleteIsAnErrorCarryingTheTypedKind(t *testing.T) {
	r, req, _ := deleteFixture(t, accepted, func(n int32) (int, string) {
		if n < 2 {
			return 200, bucketDeleting
		}
		return 200, `{"bucket":{"id":"b1","delete_state":"failed","delete_error":"bucket_still_present"}}`
	})
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed remote delete must fail the destroy (the bucket still exists)")
	}
	if got := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(got, "bucket_still_present") {
		t.Errorf("diagnostic must carry delete_error; got %q", got)
	}
}

func TestS3BucketDelete_TimesOutWhileStillDeleting(t *testing.T) {
	r, req, _ := deleteFixture(t, accepted, func(int32) (int, string) { return 200, bucketDeleting })
	err := waitS3BucketGone(context.Background(), r.client, "b1", 60*time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "deleting") {
		t.Fatalf("expected a timeout error naming the last state, got %v", err)
	}
	_ = req
}

func TestS3BucketDelete_409DeleteInProgressIsAcceptedAndPolled(t *testing.T) {
	r, req, shows := deleteFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"status":"deleting","reason":"delete_in_progress","message":"This bucket is already being deleted."}`))
	}, func(n int32) (int, string) {
		if n < 2 {
			return 200, bucketDeleting
		}
		return 404, `{}`
	})
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() || atomic.LoadInt32(shows) < 2 {
		t.Fatalf("409 delete_in_progress must be accepted and polled; diags=%v shows=%d", resp.Diagnostics, *shows)
	}
}

func TestS3BucketDelete_OtherDeleteErrorsStillFail(t *testing.T) {
	r, req, shows := deleteFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"no"}`))
	}, func(int32) (int, string) { return 200, bucketDeleting })
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() || atomic.LoadInt32(shows) != 0 {
		t.Fatalf("a refused delete must fail without polling; diags=%v shows=%d", resp.Diagnostics, *shows)
	}
}

func TestS3BucketDelete_ToleratesAFewTransientReadErrors(t *testing.T) {
	r, req, _ := deleteFixture(t, accepted, func(n int32) (int, string) {
		switch {
		case n <= 2:
			return 403, `{"message":"boom"}`
		default:
			return 404, `{}`
		}
	})
	var resp resource.DeleteResponse
	r.Delete(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("two transient read errors must not fail the destroy: %v", resp.Diagnostics)
	}
}
