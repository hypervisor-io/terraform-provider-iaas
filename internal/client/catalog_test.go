package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like ssh_key_test.go / vpc_test.go, these tests use net/http/httptest
// directly rather than internal/acctest.MockServer — acctest imports
// internal/provider which imports internal/client, so importing acctest here
// would create an import cycle.

// TestListLocations_Success verifies GET /cloud-service/locations unwraps the
// Laravel paginator {data:[...]} (locations are hypervisor groups; name is a
// slug, display_name human).
func TestListLocations_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"loc1","name":"nyc","display_name":"New York","country":"US"},{"id":"loc2","name":"lon","display_name":"London","country":"GB"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListLocations(context.Background())
	if err != nil {
		t.Fatalf("ListLocations returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/cloud-service/locations" {
		t.Errorf("path = %s; want /api/cloud-service/locations", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "loc1" || items[0]["name"] != "nyc" || items[0]["display_name"] != "New York" {
		t.Errorf("items[0] = %v; want id=loc1 name=nyc display_name=New York", items[0])
	}
}

// TestListPlanGroups_Success verifies GET
// /cloud-service/location/{id}/plan-groups unwraps a RAW top-level JSON array.
func TestListPlanGroups_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"pg1","name":"general","display_name":"General Purpose"},{"id":"pg2","name":"compute","display_name":"Compute Optimised"}]`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListPlanGroups(context.Background(), "loc1")
	if err != nil {
		t.Fatalf("ListPlanGroups returned error: %v", err)
	}
	if gotPath != "/api/cloud-service/location/loc1/plan-groups" {
		t.Errorf("path = %s; want /api/cloud-service/location/loc1/plan-groups", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "pg1" || items[0]["name"] != "general" {
		t.Errorf("items[0] = %v; want id=pg1 name=general", items[0])
	}
}

// TestListPlans_Success verifies GET
// /cloud-service/location/{id}/plan-group/{pg}/plans unwraps a RAW top-level
// JSON array (no price on the row).
func TestListPlans_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"plan1","name":"s1.small","cpu_cores":1,"ram":1024,"storage":25,"bandwidth":1000},{"id":"plan2","name":"s1.large","cpu_cores":4,"ram":8192,"storage":80,"bandwidth":4000}]`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListPlans(context.Background(), "loc1", "pg1")
	if err != nil {
		t.Fatalf("ListPlans returned error: %v", err)
	}
	if gotPath != "/api/cloud-service/location/loc1/plan-group/pg1/plans" {
		t.Errorf("path = %s; want /api/cloud-service/location/loc1/plan-group/pg1/plans", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "plan1" || items[0]["name"] != "s1.small" {
		t.Errorf("items[0] = %v; want id=plan1 name=s1.small", items[0])
	}
}

// TestSearchImages_Success verifies GET /images/search:
//   - sends search + hypervisor_group_id query params
//   - decodes the Select2 grouped envelope {results:[{text,children:[...]}]}
//   - flattens results[].children[] into a flat []map carrying id/text/distro.
func TestSearchImages_Success(t *testing.T) {
	var gotPath, gotSearch, gotHG string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSearch = r.URL.Query().Get("search")
		gotHG = r.URL.Query().Get("hypervisor_group_id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"text":"Ubuntu","children":[{"id":"img1","text":"Ubuntu 22.04","distro":"ubuntu"},{"id":"img2","text":"Ubuntu 24.04","distro":"ubuntu"}]},{"text":"Debian","children":[{"id":"img3","text":"Debian 12","distro":"debian"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.SearchImages(context.Background(), "Ubuntu", "hg9")
	if err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if gotPath != "/api/images/search" {
		t.Errorf("path = %s; want /api/images/search", gotPath)
	}
	if gotSearch != "Ubuntu" {
		t.Errorf("search query = %q; want Ubuntu", gotSearch)
	}
	if gotHG != "hg9" {
		t.Errorf("hypervisor_group_id query = %q; want hg9", gotHG)
	}
	// All three children flattened.
	if len(items) != 3 {
		t.Fatalf("len(items) = %d; want 3 (children flattened)", len(items))
	}
	if items[0]["id"] != "img1" || items[0]["text"] != "Ubuntu 22.04" || items[0]["distro"] != "ubuntu" {
		t.Errorf("items[0] = %v; want id=img1 text='Ubuntu 22.04' distro=ubuntu", items[0])
	}
	if items[2]["id"] != "img3" || items[2]["distro"] != "debian" {
		t.Errorf("items[2] = %v; want id=img3 distro=debian", items[2])
	}
}

// TestSearchImages_OmitsHypervisorGroupWhenEmpty verifies that when
// hypervisorGroupID is "", no hypervisor_group_id query param is sent.
func TestSearchImages_OmitsHypervisorGroupWhenEmpty(t *testing.T) {
	var hadHG bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadHG = r.URL.Query()["hypervisor_group_id"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.SearchImages(context.Background(), "x", ""); err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if hadHG {
		t.Error("hypervisor_group_id must NOT be sent when caller passes empty string")
	}
}

// TestListISOs_Success verifies GET /isos:
//   - sends the search query param
//   - unwraps the Laravel paginator {data:[...]}.
func TestListISOs_Success(t *testing.T) {
	var gotPath, gotSearch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSearch = r.URL.Query().Get("search")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"iso1","name":"AlmaLinux 9","filename":"alma9.iso","public":true},{"id":"iso2","name":"Rocky 9","filename":"rocky9.iso","public":true}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListISOs(context.Background(), "Alma")
	if err != nil {
		t.Fatalf("ListISOs returned error: %v", err)
	}
	if gotPath != "/api/isos" {
		t.Errorf("path = %s; want /api/isos", gotPath)
	}
	if gotSearch != "Alma" {
		t.Errorf("search query = %q; want Alma", gotSearch)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "iso1" || items[0]["filename"] != "alma9.iso" {
		t.Errorf("items[0] = %v; want id=iso1 filename=alma9.iso", items[0])
	}
}

// TestDecodeSelect2_Grouped verifies the helper flattens results[].children[].
func TestDecodeSelect2_Grouped(t *testing.T) {
	body := []byte(`{"results":[{"text":"Ubuntu","children":[{"id":"a","text":"Ubuntu 22.04"},{"id":"b","text":"Ubuntu 24.04"}]},{"text":"Debian","children":[{"id":"c","text":"Debian 12"}]}]}`)
	items, err := decodeSelect2(body)
	if err != nil {
		t.Fatalf("decodeSelect2 returned error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d; want 3", len(items))
	}
	if items[0]["id"] != "a" || items[2]["id"] != "c" {
		t.Errorf("flattened ids = %v,%v; want a,c", items[0]["id"], items[2]["id"])
	}
}

// TestDecodeSelect2_Flat verifies the helper also handles a flat results[] with
// no children (each result is itself an item).
func TestDecodeSelect2_Flat(t *testing.T) {
	body := []byte(`{"results":[{"id":"a","text":"Item A"},{"id":"b","text":"Item B"}]}`)
	items, err := decodeSelect2(body)
	if err != nil {
		t.Fatalf("decodeSelect2 returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "a" || items[1]["id"] != "b" {
		t.Errorf("ids = %v,%v; want a,b", items[0]["id"], items[1]["id"])
	}
}

// TestDecodeSelect2_Empty verifies an empty results set decodes to an empty
// (non-nil-error) slice.
func TestDecodeSelect2_Empty(t *testing.T) {
	items, err := decodeSelect2([]byte(`{"results":[]}`))
	if err != nil {
		t.Fatalf("decodeSelect2 returned error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d; want 0", len(items))
	}
}
