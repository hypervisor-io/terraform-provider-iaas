package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Catalog endpoints (verified against the real controller, routes/user_api.php).
// These are READ-ONLY lookups that back the provider's data sources. The
// envelopes VARY per endpoint, so each method routes to the right decoder:
//
//	GET /cloud-service/locations
//	    → Laravel paginator {current_page,data:[{id,name,display_name,country,…}]}
//	    (locations are hypervisor groups: name is a slug, display_name is human)
//	    → doList.
//
//	GET /cloud-service/location/{id}/plan-groups
//	    → RAW top-level JSON array [{id,name,display_name,…}] → doList.
//
//	GET /cloud-service/location/{id}/plan-group/{pg}/plans
//	    → RAW top-level JSON array [{id,name,cpu_cores,ram,storage,bandwidth,…}]
//	    (NO price on the row) → doList.
//
//	GET /images/search?search=<q>&hypervisor_group_id=<hg>
//	    → Select2 GROUPED envelope {results:[{text,children:[{id,text,distro}]}]}
//	    → custom decodeSelect2 (NOT doList): the items live in results[].children[].
//
//	GET /isos?search=<q>
//	    → Laravel paginator {current_page,data:[{id,name,filename,public,…}]}
//	    → doList.

// ListLocations returns every cloud-service location (hypervisor group) visible
// to the token. The response is a Laravel paginator, which doList unwraps.
func (c *Client) ListLocations(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/cloud-service/locations", nil)
}

// ListPlanGroups returns the plan groups offered at the given location. The
// response is a RAW top-level JSON array, which doList handles directly.
func (c *Client) ListPlanGroups(ctx context.Context, locationID string) ([]map[string]any, error) {
	path := "/cloud-service/location/" + url.PathEscape(locationID) + "/plan-groups"
	return c.doList(ctx, "GET", path, nil)
}

// ListPlans returns the plans within a (location, plan-group) pair. The response
// is a RAW top-level JSON array (no price on the row), which doList handles.
func (c *Client) ListPlans(ctx context.Context, locationID, planGroupID string) ([]map[string]any, error) {
	path := "/cloud-service/location/" + url.PathEscape(locationID) +
		"/plan-group/" + url.PathEscape(planGroupID) + "/plans"
	return c.doList(ctx, "GET", path, nil)
}

// SearchImages searches the image catalogue. The endpoint returns a Select2
// GROUPED envelope ({results:[{text,children:[…]}]}); decodeSelect2 flattens
// results[].children[] (or the bare results[] when a result has no children)
// into a flat []map carrying each child's id, text/name, and distro.
//
// hypervisorGroupID is optional: when "" it is omitted from the query so the
// controller searches across all groups the token can see.
func (c *Client) SearchImages(ctx context.Context, query, hypervisorGroupID string) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("search", query)
	if hypervisorGroupID != "" {
		q.Set("hypervisor_group_id", hypervisorGroupID)
	}
	path := "/images/search?" + q.Encode()

	resp, raw, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeSelect2(raw)
}

// ListISOs searches the ISO catalogue by name. The response is a Laravel
// paginator, which doList unwraps. query is sent as the ?search= param.
func (c *Client) ListISOs(ctx context.Context, query string) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("search", query)
	return c.doList(ctx, "GET", "/isos?"+q.Encode(), nil)
}

// decodeSelect2 unwraps a Select2 envelope ({"results":[…]}) into a flat list.
//
// Select2 has two shapes:
//   - GROUPED: each result is an optgroup carrying a "children" array — the real
//     items live in results[].children[]. We flatten all children.
//   - FLAT: each result IS an item (no "children" key) — we take the result
//     itself.
//
// A result with a present "children" array contributes its children; a result
// without one contributes itself. This makes the helper reusable by the later
// Select2-backed data sources (image search today; region/lb_plan/db_plan/etc.
// in their tiers).
func decodeSelect2(body []byte) ([]map[string]any, error) {
	var env struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decoding Select2 response: %w", err)
	}

	out := make([]map[string]any, 0, len(env.Results))
	for i, result := range env.Results {
		childrenRaw, hasChildren := result["children"]
		if !hasChildren {
			// Flat result — the result itself is an item.
			out = append(out, result)
			continue
		}
		children, ok := childrenRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("Select2 results[%d].children is not an array (got %T)", i, childrenRaw)
		}
		for j, ch := range children {
			obj, ok := ch.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Select2 results[%d].children[%d] is not an object (got %T)", i, j, ch)
			}
			out = append(out, obj)
		}
	}
	return out, nil
}
