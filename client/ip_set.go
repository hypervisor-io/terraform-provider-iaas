package client

import (
	"context"
	"fmt"
	"net/url"
)

// IP Set endpoints (verified against the real UserApi\IpSetController +
// routes/user_api.php). An IP set is a named, ip_version-scoped collection of
// CIDR entries used by security-group rules. The entries are managed as child
// rows under the parent ip_set.
//
// Parent (ip_set) CRUD - note the plural-vs-singular path asymmetry:
//
//	INDEX   GET    /ip-sets               (PLURAL)  → Laravel paginator {data:[...]}
//	                                       each row carries entries_count, rules_count
//	CREATE  POST   /ip-sets               (PLURAL)  body {name (required),
//	                                       description?, ip_version (required: ipv4|ipv6)}
//	                                       → 200 {success,message,ip_set:{id,name,...}}
//	SHOW    GET    /ip-set/{id}           (SINGULAR) → 200 {success,ip_set:{...,
//	                                       entries:[{id,cidr,description},...]}};
//	                                       entries are EMBEDDED so Read can hydrate them
//	UPDATE  PATCH  /ip-set/{id}           (SINGULAR) body {name (required),
//	                                       description?, ip_version?} → 200 {success,message}
//	                                       (NOTE: the UPDATE response carries NO ip_set body -
//	                                        the resource must Read back after updating)
//	DELETE  DELETE /ip-set/{id}           (SINGULAR) → 200 {success,message};
//	                                       200 success:false when in use by a rule (C3)
//
// Entry operations (children of an ip_set):
//
//	ADD     POST   /ip-set/{id}/entries   body {cidr (required), description?}
//	                                       → 200 {success,message,entry:{id,cidr,description}}
//	                                       Preserves the per-entry description.
//	BULK    POST   /ip-set/{id}/bulk-add   body {cidrs:["1.2.3.0/24",...]}
//	                                       → 200 {success,message,created:[{id,cidr}],errors:[]}
//	                                       NOTE: bulk-add only accepts CIDR STRINGS and DROPS
//	                                       any per-entry description. The created objects also
//	                                       lack a description. The ip_set RESOURCE therefore
//	                                       adds entries one-by-one via AddEntry so comments
//	                                       round-trip; BulkAddEntries is provided for callers
//	                                       that do not need per-entry descriptions.
//	REMOVE  DELETE /ip-set/{id}/entry/{entryId}  (SINGULAR entry path)
//	                                       → 200 {success,message}; 200 success:false on failure
//
// Notes:
//   - All writes are SYNCHRONOUS at HTTP 200 (no task/state, no waiter).
//   - Failure is signalled with success:false at HTTP 200 (e.g. duplicate cidr,
//     invalid cidr, global ip_set, in-use on delete). doItem/doVoid surface this.
//   - Empty-id guards are applied on every path-id argument (consistency).
//   - Every id is url.PathEscape'd into the path.

// ListIPSets returns all IP sets visible to the authenticated user
// (own + global), paginator-aware.
func (c *Client) ListIPSets(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/ip-sets", nil)
}

// CreateIPSet creates an IP set from the supplied body (name + ip_version
// required; description optional). The create is synchronous: the returned
// object carries the id. The collection path is PLURAL (/ip-sets).
func (c *Client) CreateIPSet(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/ip-sets", body, "ip_set")
}

// GetIPSet fetches a single IP set by id. The SHOW route is SINGULAR
// (/ip-set/{id}) and returns the ip_set with its entries EMBEDDED. A 404
// (absent / belonging to another user) is an *APIError recognised by IsNotFound.
func (c *Client) GetIPSet(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetIPSet: empty id")
	}
	path := "/ip-set/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "ip_set")
}

// UpdateIPSet patches the mutable scalar fields of an IP set (name required;
// description/ip_version optional). The UPDATE route is SINGULAR. The PATCH
// response carries NO ip_set body (only {success,message}), so the resource
// must call GetIPSet afterwards to refresh state - this method returns the bare
// envelope map for completeness but callers should not rely on it carrying id.
func (c *Client) UpdateIPSet(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateIPSet: empty id")
	}
	path := "/ip-set/" + url.PathEscape(id)
	// key="" → return the bare envelope (no ip_set wrapper on UPDATE).
	return c.doItem(ctx, "PATCH", path, fields, "")
}

// DeleteIPSet deletes an IP set by id (and cascades its entries). The DELETE
// route is SINGULAR. A failure is signalled with success:false at HTTP 200
// (e.g. the set is referenced by a security-group rule), so doVoid checks it.
func (c *Client) DeleteIPSet(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteIPSet: empty id")
	}
	path := "/ip-set/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// AddIPSetEntry adds a single CIDR entry to an IP set, preserving the optional
// description. body must contain "cidr"; "description" is optional. The response
// is unwrapped from the "entry" envelope and carries the new entry's id (needed
// so the resource can later delete this exact entry). This is the per-entry add
// the resource uses (bulk-add drops descriptions).
func (c *Client) AddIPSetEntry(ctx context.Context, setID string, body map[string]any) (map[string]any, error) {
	if setID == "" {
		return nil, fmt.Errorf("AddIPSetEntry: empty setID")
	}
	path := "/ip-set/" + url.PathEscape(setID) + "/entries"
	return c.doItem(ctx, "POST", path, body, "entry")
}

// BulkAddIPSetEntries adds many CIDR entries at once via the /bulk-add endpoint.
// The endpoint accepts only an array of CIDR STRINGS (key "cidrs") and DROPS any
// per-entry description, so it is provided for callers that do not need
// descriptions. The response is the bare envelope {success,message,created,errors};
// "created" holds the freshly-created entry objects (id + cidr only). cidrs must
// be non-empty (the controller validates min:1).
func (c *Client) BulkAddIPSetEntries(ctx context.Context, setID string, cidrs []string) (map[string]any, error) {
	if setID == "" {
		return nil, fmt.Errorf("BulkAddIPSetEntries: empty setID")
	}
	path := "/ip-set/" + url.PathEscape(setID) + "/bulk-add"
	body := map[string]any{"cidrs": cidrs}
	// key="" → bare envelope (created/errors live at the top level).
	return c.doItem(ctx, "POST", path, body, "")
}

// DeleteIPSetEntry removes a single entry from an IP set by entry id. The route
// is DELETE /ip-set/{setID}/entry/{entryID} (singular "entry"). A failure is
// signalled with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteIPSetEntry(ctx context.Context, setID, entryID string) error {
	if setID == "" {
		return fmt.Errorf("DeleteIPSetEntry: empty setID")
	}
	if entryID == "" {
		return fmt.Errorf("DeleteIPSetEntry: empty entryID")
	}
	path := "/ip-set/" + url.PathEscape(setID) + "/entry/" + url.PathEscape(entryID)
	return c.doVoid(ctx, "DELETE", path, nil)
}
