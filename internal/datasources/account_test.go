package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// accountProfileBody mirrors the real GET /profile envelope
// (UserApi\ProfileController::show): {"success":true,"data":{...}}. Field
// values/shapes match the controller's documented Scribe example, including
// is_admin/self_provisioning as JSON integers (0/1) rather than booleans,
// which boolField coerces.
const accountProfileBody = `{
  "success": true,
  "data": {
    "id": "97b61cd3-612a-4e96-94d9-db0af769917f",
    "first_name": "John",
    "last_name": "Doe",
    "email": "john@example.com",
    "company_name": "Acme Corp",
    "status": 1,
    "is_admin": 0,
    "timezone": "America/New_York",
    "default_currency": "USD",
    "two_factor_enabled": 0,
    "self_provisioning": 1,
    "owner_id": null,
    "last_login_at": "2024-05-21T10:30:00.000000Z",
    "created_at": "2024-01-15T08:20:00.000000Z",
    "updated_at": "2024-05-21T10:30:00.000000Z",
    "gravatar": "https://www.gravatar.com/avatar/abc123"
  }
}`

// TestUnitAccount_whoami - the singleton account data source has no input
// filter; Read calls GET /profile and every attribute resolves from the mock
// response, including the derived id used elsewhere in a config
// (data.iaas_account.current.id).
func TestUnitAccount_whoami(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(accountProfileBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_account" "current" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_account.current", "id", "97b61cd3-612a-4e96-94d9-db0af769917f"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "first_name", "John"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "last_name", "Doe"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "email", "john@example.com"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "company_name", "Acme Corp"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "status", "1"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "is_admin", "false"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "timezone", "America/New_York"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "default_currency", "USD"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "two_factor_enabled", "false"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "self_provisioning", "true"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "owner_id", ""),
					resource.TestCheckResourceAttr("data.iaas_account.current", "gravatar", "https://www.gravatar.com/avatar/abc123"),
				),
			},
		},
	})
}

// TestUnitAccount_subuser - owner_id populates when the profile belongs to a
// subuser, confirming the subuser-flag mapping the plan calls out.
func TestUnitAccount_subuser(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"sub-1","email":"sub@example.com","owner_id":"owner-1"}}`))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_account" "current" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_account.current", "id", "sub-1"),
					resource.TestCheckResourceAttr("data.iaas_account.current", "owner_id", "owner-1"),
				),
			},
		},
	})
}

// TestUnitAccount_unauthorized - a 401 from /profile (bad/IP-mismatched
// token) surfaces the shared IP-lock diagnostic hint, letting a config fail
// fast on a misconfigured provider token.
func TestUnitAccount_unauthorized(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Unauthorized!"}`))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_account" "current" {}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`(?i)IP address`),
			},
		},
	})
}
