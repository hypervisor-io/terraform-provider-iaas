package client

import "context"

// Account (whoami) endpoint.
//
// DEVIATION FROM PLAN (2026-07-05-opentofu-provider-waves-bc.md, T1): the plan
// named GET /connect (UserApi\AuthController@check) as the whoami source to
// build this data source against. Inspecting the actual controller
// (app/Http/Controllers/UserApi/AuthController.php in the Master repo) shows
// check() returns ONLY {"success":true,"message":"Token is valid!"} - it
// validates the bearer token (and the UserApiAuthentication middleware's
// IP-lock) and echoes a fixed, localized string. There is NO id/name/email/
// is_admin/subuser field anywhere in that response. Building the data source
// against /connect literally would leave it with nothing meaningful to read
// (data.iaas_account.current.id would not exist), directly contradicting the
// plan's own "Value" bullet ("read data.iaas_account.current.id").
//
// GET /profile (UserApi\ProfileController@show, also under routes/user_api.php)
// is the actual authenticated-whoami endpoint: it returns the caller's full
// account object - {"success":true,"data":{id,first_name,last_name,email,
// company_name,status,is_admin,timezone,default_currency,two_factor_enabled,
// self_provisioning,owner_id,last_login_at,created_at,updated_at,gravatar,
// consumed_credit,consumed_hours}} - which is exactly the field set the plan
// asks the data source to expose. GetAccount targets /profile instead of
// /connect for that reason.
//
// 401/403 handling (the IP-lock/scope diagnostic hint) is centralized in
// responseError (internal/client/errors.go), not per-route, so the plan's
// "surfaces the IP-lock diagnostic on 401/403" requirement is satisfied
// identically regardless of which authenticated endpoint is called.
//
// owner_id is the account's subuser indicator: it is present (non-empty) when
// the token belongs to a subuser invited by another account, and empty when
// the token belongs to the account owner itself - this is the "subuser flag"
// the plan asks to surface if present.

// GetAccount fetches the authenticated caller's own account (whoami). The
// response wraps the account object under the "data" key.
func (c *Client) GetAccount(ctx context.Context) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/profile", nil, "data")
}
