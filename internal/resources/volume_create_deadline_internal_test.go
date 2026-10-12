package resources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/volumeop"
)

// TestCreateVolumeAndAttach_OneDeadlineForTheWholeCreate pins that the create
// timeout bounds provisioning AND the optional confirmed attach together.
// Provisioning takes ~400 ms and the attach never completes; with an 800 ms
// create timeout the whole call must end near 800 ms. Giving the attach a
// fresh full budget (the reviewed defect) ends near 1200 ms.
func TestCreateVolumeAndAttach_OneDeadlineForTheWholeCreate(t *testing.T) {
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "5ms")

	var mu sync.Mutex
	var created time.Time
	active := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		vol := map[string]any{"id": "vol-1", "status": "pending", "instance_id": nil, "active_operation": nil}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/storage/volumes":
			created = time.Now()
		case r.Method == http.MethodPost && r.URL.Path == "/storage/volume/vol-1/attach":
			active = "attach" // recorded; the node never confirms
		}
		if !created.IsZero() && time.Since(created) >= 400*time.Millisecond {
			vol["status"] = "available"
		}
		if active != "" {
			vol["active_operation"] = active
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "volume": vol})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "tok", 5*time.Second, false)

	start := time.Now()
	_, id, summary, err := createVolumeAndAttach(context.Background(), c,
		map[string]any{"name": "data"}, "inst-1", 800*time.Millisecond, func(string) bool { return true })
	elapsed := time.Since(start)

	if id != "vol-1" || summary != "Error attaching volume" {
		t.Fatalf("id=%q summary=%q err=%v; want the attach stage to fail", id, summary, err)
	}
	var pending *volumeop.PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v (%T); want *volumeop.PendingError", err, err)
	}
	if elapsed > 1050*time.Millisecond {
		t.Fatalf("create took %v; provisioning and attach must share ONE 800ms deadline", elapsed)
	}
}
