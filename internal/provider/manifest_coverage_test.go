package provider_test

// Tri-sync CI gate, provider leg (spec 17 REQ-TRISYNC-03 check 2). It reads the
// vendored copy of the platform's api-manifest.json (kept in sync from the
// Master repo via `make sync-manifest`) and asserts that every endpoint the
// manifest marks opentofu.status=="covered" names an opentofu.type that this
// provider actually registers. A covered type that is not registered is a real
// tri-sync drift bug and fails the build. When TRISYNC_RELEASE=1 it also fails
// on any opentofu.status=="pending" (the release gate: a release may not ship
// with an endpoint the provider neither covers nor explicitly excludes).

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/provider"
)

const manifestPath = "testdata/api-manifest.json"

type manifestEndpoint struct {
	ID       string `json:"id"`
	Surface  string `json:"surface"`
	OpenTofu struct {
		Status string `json:"status"`
		Type   string `json:"type"`
	} `json:"opentofu"`
}

type manifest struct {
	Endpoints []manifestEndpoint `json:"endpoints"`
}

// registeredTypes enumerates every iaas_* resource + data-source type the
// provider registers, by invoking each factory's Metadata (the same call the
// framework makes at runtime to learn a type's name).
func registeredTypes(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	p := provider.New("test")()
	types := map[string]bool{}

	for _, f := range p.Resources(ctx) {
		var resp resource.MetadataResponse
		f().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "iaas"}, &resp)
		types[resp.TypeName] = true
	}
	for _, f := range p.DataSources(ctx) {
		var resp datasource.MetadataResponse
		f().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "iaas"}, &resp)
		types[resp.TypeName] = true
	}
	return types
}

// checkCoverage is the reusable assertion the positive and negative tests share.
// It returns the sorted list of covered-but-unregistered types (empty == pass)
// and, separately, the pending endpoint ids.
func checkCoverage(registered map[string]bool, m manifest) (missing, pending []string) {
	missingSet := map[string]bool{}
	for _, e := range m.Endpoints {
		switch e.OpenTofu.Status {
		case "covered":
			if e.OpenTofu.Type == "" || !registered[e.OpenTofu.Type] {
				missingSet[e.OpenTofu.Type+"  (e.g. "+e.ID+")"] = true
			}
		case "pending":
			pending = append(pending, e.ID)
		}
	}
	for k := range missingSet {
		missing = append(missing, k)
	}
	sort.Strings(missing)
	sort.Strings(pending)
	return missing, pending
}

func loadManifest(t *testing.T, path string) manifest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading manifest %s: %v", path, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing manifest %s: %v", path, err)
	}
	if len(m.Endpoints) == 0 {
		t.Fatalf("manifest %s has no endpoints", path)
	}
	return m
}

func TestManifestOpenTofuCoverage(t *testing.T) {
	registered := registeredTypes(t)
	if len(registered) == 0 {
		t.Fatal("provider registered zero types")
	}
	m := loadManifest(t, manifestPath)

	missing, pending := checkCoverage(registered, m)

	// Count for visibility.
	covered := 0
	for _, e := range m.Endpoints {
		if e.OpenTofu.Status == "covered" {
			covered++
		}
	}
	t.Logf("provider registers %d iaas_* types; manifest has %d opentofu-covered endpoints across %d total",
		len(registered), covered, len(m.Endpoints))

	if len(missing) > 0 {
		t.Fatalf("manifest marks these opentofu types 'covered' but the provider does not register them (tri-sync drift):\n  %s",
			strings.Join(missing, "\n  "))
	}

	// Release gate: no pending allowed when TRISYNC_RELEASE=1.
	if os.Getenv("TRISYNC_RELEASE") == "1" && len(pending) > 0 {
		t.Fatalf("TRISYNC_RELEASE=1 but %d endpoints are opentofu.status=pending (release blocked):\n  %s",
			len(pending), strings.Join(pending, "\n  "))
	}
}

// TestManifestOpenTofuCoverage_NegativeDetectsPhantom proves the check actually
// fails when the manifest claims a covered type the provider does not register.
func TestManifestOpenTofuCoverage_NegativeDetectsPhantom(t *testing.T) {
	registered := registeredTypes(t)
	phantom := manifest{Endpoints: []manifestEndpoint{
		{ID: "user POST /api/synthetic", Surface: "user"},
	}}
	phantom.Endpoints[0].OpenTofu.Status = "covered"
	phantom.Endpoints[0].OpenTofu.Type = "iaas_does_not_exist"

	missing, _ := checkCoverage(registered, phantom)
	if len(missing) == 0 {
		t.Fatal("negative test failed: checkCoverage did not flag the phantom iaas_does_not_exist type")
	}
}
