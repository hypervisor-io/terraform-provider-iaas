package tfdiag

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestFromErr_BusyAndDatabaseReasons(t *testing.T) {
	cases := []struct {
		reason string
		status int
		want   string
	}{
		{client.ReasonAddressReservationBusy, 409, "safe to run the same command again"},
		{client.ReasonDatabaseBusy, 409, "safe to run the same command again"},
		{client.ReasonDatabaseError, 500, "internal database error"},
	}
	retryAfter := map[string]time.Duration{client.ReasonAddressReservationBusy: time.Second, client.ReasonDatabaseBusy: time.Second}
	for _, tc := range cases {
		d := FromErr("Error", fmt.Errorf("wrap: %w", &client.APIError{Status: tc.status, Message: "server sentence", Reason: tc.reason, RetryAfter: retryAfter[tc.reason]}))
		if d.Summary() != "Error" {
			t.Fatalf("summary %q", d.Summary())
		}
		det := d.Detail()
		if !strings.Contains(det, "server sentence") || !strings.Contains(det, tc.want) || !strings.Contains(det, "(reason: "+tc.reason+")") {
			t.Errorf("%s: detail = %q", tc.reason, det)
		}
	}
	// the managed-database admission reuses the reason database_busy WITHOUT Retry-After: a real conflict
	d := FromErr("Error", &client.APIError{Status: 409, Message: "Another operation is running on this database.", Reason: client.ReasonDatabaseBusy})
	if strings.Contains(d.Detail(), "safe to run") {
		t.Errorf("managed-database database_busy carries the busy hint: %q", d.Detail())
	}
	// a real conflict gets no retry hint
	d = FromErr("Error", &client.APIError{Status: 409, Message: "in use", Reason: "plan_in_use"})
	if strings.Contains(d.Detail(), "safe to run") {
		t.Errorf("plan_in_use carries the busy hint: %q", d.Detail())
	}
}
