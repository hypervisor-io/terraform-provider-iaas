package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (importing acctest here would create an
// import cycle: acctest → provider → client). The shared `contains` helper lives
// in ssh_key_test.go.

// ---------------------------------------------------------------------------
// GetKubeconfig
// ---------------------------------------------------------------------------

// TestGetKubeconfig_Success verifies GetKubeconfig:
//   - GETs /kubernetes/cluster/{id}/kubeconfig
//   - returns the RAW YAML body verbatim (NOT JSON-decoded), because the real
//     KubeconfigController returns application/yaml with a freshly-minted
//     admin client cert inline (sensitive).
func TestGetKubeconfig_Success(t *testing.T) {
	const yaml = "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://10.0.1.5:6443\n    certificate-authority-data: Q0FDRVJU\n  name: prod\nusers:\n- name: kubernetes-admin\n  user:\n    client-certificate-data: Q0xJRU5U\n    client-key-data: S0VZ\n"

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(yaml))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	got, err := c.GetKubeconfig(context.Background(), "k8s-uuid-1")
	if err != nil {
		t.Fatalf("GetKubeconfig returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/kubeconfig" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/kubeconfig", gotPath)
	}
	if got != yaml {
		t.Errorf("body = %q; want raw YAML verbatim %q", got, yaml)
	}
}

// TestGetKubeconfig_NotBootstrapped verifies that a 404 JSON error body (cluster
// has not finished bootstrap - no CA yet) surfaces as an *APIError recognised by
// IsNotFound.
func TestGetKubeconfig_NotBootstrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"kubeconfig not yet available - cluster has not finished bootstrap. Retry after cluster reaches running state."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubeconfig(context.Background(), "k8s-uuid-1")
	if err == nil {
		t.Fatal("GetKubeconfig: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 response, err = %v", err)
	}
}

// TestGetKubeconfig_EmptyID verifies the empty-id guard short-circuits before any
// request.
func TestGetKubeconfig_EmptyID(t *testing.T) {
	c := New("http://example.invalid/api", "tok", time.Second, false)
	_, err := c.GetKubeconfig(context.Background(), "")
	if err == nil {
		t.Fatal("GetKubeconfig(\"\"): expected error, got nil")
	}
	if !contains(err.Error(), "empty clusterID") {
		t.Errorf("error = %v; want it to mention empty clusterID", err)
	}
}

// ---------------------------------------------------------------------------
// GetAutoscalerManifest
// ---------------------------------------------------------------------------

// TestGetAutoscalerManifest_Success verifies GetAutoscalerManifest:
//   - GETs /kubernetes/cluster/{id}/autoscaler-manifest
//   - returns the RAW YAML body verbatim (NOT JSON), matching the controller's
//     text/yaml manifest (embeds a base64 controller JWT inline - sensitive).
func TestGetAutoscalerManifest_Success(t *testing.T) {
	const manifest = "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: cluster-autoscaler\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: cluster-autoscaler-token\ndata:\n  token: SldUX0JBU0U2NA==\n"

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(manifest))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	got, err := c.GetAutoscalerManifest(context.Background(), "k8s-uuid-1")
	if err != nil {
		t.Fatalf("GetAutoscalerManifest returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/autoscaler-manifest" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/autoscaler-manifest", gotPath)
	}
	if got != manifest {
		t.Errorf("body = %q; want raw YAML verbatim %q", got, manifest)
	}
}

// TestGetAutoscalerManifest_NotEnabled verifies that a 422 JSON error
// (autoscaling not enabled) surfaces as an error carrying the message.
func TestGetAutoscalerManifest_NotEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"autoscaling not enabled on this cluster"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetAutoscalerManifest(context.Background(), "k8s-uuid-1")
	if err == nil {
		t.Fatal("GetAutoscalerManifest: expected error for 422, got nil")
	}
	if !contains(err.Error(), "autoscaling not enabled") {
		t.Errorf("error = %v; want it to mention autoscaling not enabled", err)
	}
}

// ---------------------------------------------------------------------------
// Catalog search (Select2)
// ---------------------------------------------------------------------------

// TestSearchK8sVersions_Success verifies SearchK8sVersions:
//   - GETs /kubernetes/search/versions with the ?search= filter
//   - flattens the FLAT Select2 envelope ({results:[{id,text,semantic_version}]})
//     to a []map carrying id + text.
func TestSearchK8sVersions_Success(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("search")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"id":"ver-1","text":"1.31.4","semantic_version":"1.31.4"},{"id":"ver-2","text":"1.30.8","semantic_version":"1.30.8"}],"pagination":{"more":false}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.SearchK8sVersions(context.Background(), "1.31")
	if err != nil {
		t.Fatalf("SearchK8sVersions returned error: %v", err)
	}
	if gotPath != "/api/kubernetes/search/versions" {
		t.Errorf("path = %s; want /api/kubernetes/search/versions", gotPath)
	}
	if gotQuery != "1.31" {
		t.Errorf("search = %q; want 1.31", gotQuery)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items; want 2 (flat Select2)", len(items))
	}
	if items[0]["id"] != "ver-1" || items[0]["text"] != "1.31.4" {
		t.Errorf("items[0] = %v; want id=ver-1 text=1.31.4", items[0])
	}
}

// TestSearchK8sVersions_NoQuery verifies that an empty query omits the ?search=
// param entirely so the controller lists everything.
func TestSearchK8sVersions_NoQuery(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[],"pagination":{"more":false}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.SearchK8sVersions(context.Background(), ""); err != nil {
		t.Fatalf("SearchK8sVersions returned error: %v", err)
	}
	if gotRawQuery != "" {
		t.Errorf("raw query = %q; want empty (no ?search= when query is blank)", gotRawQuery)
	}
}

// TestSearchK8sRegions_Success verifies SearchK8sRegions hits the regions route
// and flattens the flat Select2 envelope including the slug/feature fields.
func TestSearchK8sRegions_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"id":"hg-1","text":"NYC1","slug":"nyc1","kubernetes_enabled":1,"vpc_enabled":1,"lb_enabled":1}],"pagination":{"more":false}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.SearchK8sRegions(context.Background(), "nyc")
	if err != nil {
		t.Fatalf("SearchK8sRegions returned error: %v", err)
	}
	if gotPath != "/api/kubernetes/search/regions" {
		t.Errorf("path = %s; want /api/kubernetes/search/regions", gotPath)
	}
	if len(items) != 1 || items[0]["id"] != "hg-1" || items[0]["slug"] != "nyc1" {
		t.Errorf("items = %v; want one region id=hg-1 slug=nyc1", items)
	}
}

// TestSearchK8sPlans_Routes verifies the three plan-search helpers each hit their
// distinct route (worker /plans, cp /cp-plans, lb /lb-plans) and flatten the flat
// Select2 envelope, carrying the name + spec fields.
func TestSearchK8sPlans_Routes(t *testing.T) {
	cases := []struct {
		name     string
		call     func(c *Client) ([]map[string]any, error)
		wantPath string
		body     string
		wantName string
	}{
		{
			name:     "worker",
			call:     func(c *Client) ([]map[string]any, error) { return c.SearchK8sWorkerPlans(context.Background(), "std") },
			wantPath: "/api/kubernetes/search/plans",
			body:     `{"results":[{"id":"ip-1","text":"std-2 - 2 CPU, 4096 MB, 80 GB","name":"std-2","cpu_cores":2,"ram":4096,"storage":80,"credit_value":1000}],"pagination":{"more":false}}`,
			wantName: "std-2",
		},
		{
			name: "control-plane",
			call: func(c *Client) ([]map[string]any, error) {
				return c.SearchK8sControlPlanePlans(context.Background(), "cp")
			},
			wantPath: "/api/kubernetes/search/cp-plans",
			body:     `{"results":[{"id":"ip-cp","text":"cp-2 - 2 CPU, 4096 MB, 40 GB","name":"cp-2","cpu_cores":2,"ram":4096,"storage":40,"credit_value":1500}],"pagination":{"more":false}}`,
			wantName: "cp-2",
		},
		{
			name: "load-balancer",
			call: func(c *Client) ([]map[string]any, error) {
				return c.SearchK8sLoadBalancerPlans(context.Background(), "lb")
			},
			wantPath: "/api/kubernetes/search/lb-plans",
			body:     `{"results":[{"id":"lbp-1","text":"lb-2 - 2 CPU, 2048 MB","name":"lb-2","cpu_cores":2,"ram":2048,"credit_value":500}],"pagination":{"more":false}}`,
			wantName: "lb-2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := New(srv.URL+"/api", "tok", 10*time.Second, false)
			items, err := tc.call(c)
			if err != nil {
				t.Fatalf("search returned error: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s; want %s", gotPath, tc.wantPath)
			}
			if len(items) != 1 || items[0]["name"] != tc.wantName {
				t.Errorf("items = %v; want one plan name=%s", items, tc.wantName)
			}
		})
	}
}
