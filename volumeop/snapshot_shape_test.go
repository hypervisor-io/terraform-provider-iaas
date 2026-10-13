package volumeop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Shape pins on SnapshotIDsNamed (from the round 2 review, MR5).

// A JSON null name is not the empty string: FindVolumeSnapshotByName requires
// the type assertion to succeed (and refuses an empty name outright), so the
// pre-send check should agree, or a name="" create is refused by every
// null-named row while the resolve would never match one.
func TestShape_SnapshotIDsNamed_NullNameIsNotEmptyString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"volume": map[string]any{"id": "vol-1", "snapshots": []any{
			map[string]any{"id": "s-null", "name": nil, "status": "available"},
			map[string]any{"id": "s-nokey", "status": "available"},
			map[string]any{"id": "s-num", "name": 7, "status": "available"},
		}}})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "t", 2*time.Second, false)
	ids, err := SnapshotIDsNamed(context.Background(), c, "vol-1", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("name=\"\" matched ids %v", ids)
	if len(ids) != 0 {
		t.Fatalf("name=\"\" matched %v; a null or missing name is not the empty string", ids)
	}
}

// The volume SHOW embeds every snapshot (the Master's VolumeController::show
// loads the relation with ->get(), no pagination); the helper must read the
// whole list, a match at the end included.
func TestShape_SnapshotIDsNamed_ReadsTheWholeEmbeddedList(t *testing.T) {
	const n = 750
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := make([]any, 0, n)
		for i := 0; i < n; i++ {
			list = append(list, map[string]any{"id": fmt.Sprintf("s-%04d", i), "name": fmt.Sprintf("other-%d", i)})
		}
		list[n-1].(map[string]any)["name"] = "wanted"
		list[0].(map[string]any)["name"] = "wanted"
		writeJSON(w, http.StatusOK, map[string]any{"volume": map[string]any{"id": "vol-1", "snapshots": list}})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "t", 2*time.Second, false)
	ids, err := SnapshotIDsNamed(context.Background(), c, "vol-1", "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "s-0000" || ids[1] != fmt.Sprintf("s-%04d", n-1) {
		t.Fatalf("ids = %v; want the first and the last entry", ids)
	}
}
