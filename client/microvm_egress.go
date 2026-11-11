package client

import (
	"context"
	"fmt"
	"net/url"
)

// Sandbox outbound network policy (user_api, bearer token).
//
//	READ    GET    /microvm/vm/{id}/egress                -> {success,egress:{policy,generation,
//	                                                          applied_generation,state,applied_at,
//	                                                          last_error_code,capability:{available,names},
//	                                                          service_ports:[{proto,port}]}}
//	REPLACE PUT    /microvm/vm/{id}/egress   body = the policy document
//	                                                       -> the READ body; 202 while the node has not
//	                                                          confirmed the new generation, 200 once it has
//	REMOVE  DELETE /microvm/vm/{id}/egress                -> the READ body with policy null; 202 or 200
//
// The policy document is {default_action: allow|deny, inbound_replies?:
// service_ports|any, rules: [{action, target, protocol?, ports?, comment?,
// id?}]}. policy is null in the read model when the sandbox has no policy
// (unrestricted). A write never waits for the node: read `state`
// (none|pending|applied|failed|unavailable) to follow it. A killed or errored
// sandbox reads `inactive` while a policy is stored (kept for reading, in force
// nowhere) and `none` when none is; the same value is in the `egress` summary
// of the sandbox read and list.
//
// Refusals: 422 with a typed reason (invalid_egress_policy,
// egress_target_invalid, egress_target_not_permitted,
// egress_wildcard_too_broad, egress_dns_port_not_allowed,
// egress_rule_limit_exceeded, egress_names_not_supported) and field errors
// relative to the policy; 409 egress_policy_terminal (killed or errored
// sandbox) or egress_policy_unsupported_node; 404 sandbox_not_found.

func microvmEgress(id string) string { return microvm(id) + "/egress" }

// GetMicrovmEgress returns the sandbox's outbound policy read model (the
// object under "egress"). Its "policy" is nil when no policy is set.
func (c *Client) GetMicrovmEgress(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmEgress: empty id")
	}
	return c.microvmEgressItem(ctx, "GET", id, nil)
}

// SetMicrovmEgress replaces the sandbox's whole outbound policy with policy
// (the document itself is the request body) and returns the read model as
// stored: rule ids assigned, names lower-cased, protocol and inbound_replies
// defaulted. Both 200 (node confirmed) and 202 (stored, node still owed) are
// success; the caller reads "state" to tell them apart.
func (c *Client) SetMicrovmEgress(ctx context.Context, id string, policy map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("SetMicrovmEgress: empty id")
	}
	if policy == nil {
		return nil, fmt.Errorf("SetMicrovmEgress: nil policy (use ClearMicrovmEgress to remove a policy)")
	}
	return c.microvmEgressItem(ctx, "PUT", id, policy)
}

// ClearMicrovmEgress removes the sandbox's outbound policy (back to
// unrestricted) and returns the read model. Removing a policy that is not
// set is a no-op answered 200.
func (c *Client) ClearMicrovmEgress(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("ClearMicrovmEgress: empty id")
	}
	return c.microvmEgressItem(ctx, "DELETE", id, nil)
}

// microvmEgressItem performs one policy call and unwraps the read model. A
// 2xx answer without an "egress" object is an error, never an empty policy:
// drift detection must not mistake a malformed answer for "no policy".
func (c *Client) microvmEgressItem(ctx context.Context, method, id string, body any) (map[string]any, error) {
	envelope, err := c.doItem(ctx, method, microvmEgress(id), body, "")
	if err != nil {
		return nil, err
	}
	egress, ok := envelope["egress"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s %s: response has no egress object", method, microvmEgress(id))
	}
	return egress, nil
}

// Sandbox outbound refusal log (user_api, bearer token).
//
//	GET /microvm/vm/{id}/egress/log?layer=&class=&rule_id=&search=&since=&until=&page=&per_page=
//	    -> {success, events: {current_page, data: [...], per_page, total, last_page, ...},
//	        collection: {pulled_at, packet_layer, retention_days, max_rows}}
//
// What the sandbox's policy refused, newest first, as the platform has stored
// it (collected from the node about once a minute and once more before a
// stop, kill or delete; readable after a kill). Each entry is an aggregate:
// {id, layer dns|packet, class, rule, rule_id, category, destination,
// withheld, protocol, port, qtype, count, sampled, suppressed_keys, first_at,
// last_at}. per_page is capped at 100 by the platform. A bad filter value is
// a 422 with field errors; an unknown, deleted or foreign sandbox is a 404
// sandbox_not_found.

// MicrovmEgressLogFilters are the query parameters GetMicrovmEgressLog may
// send. Any other key is dropped, so a caller cannot smuggle a parameter the
// endpoint does not document.
var MicrovmEgressLogFilters = []string{"layer", "class", "rule_id", "search", "since", "until", "page", "per_page"}

// GetMicrovmEgressLog returns one page of the sandbox's refusal log: the
// whole answer, with the page under "events" (its rows under "data") and how
// the log is collected under "collection". Empty filter values are not sent.
// A 2xx answer without an "events" object is an error, never an empty log.
func (c *Client) GetMicrovmEgressLog(ctx context.Context, id string, filters map[string]string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmEgressLog: empty id")
	}
	q := url.Values{}
	for _, key := range MicrovmEgressLogFilters {
		if v := filters[key]; v != "" {
			q.Set(key, v)
		}
	}
	path := microvmEgress(id) + "/log"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	envelope, err := c.doItem(ctx, "GET", path, nil, "")
	if err != nil {
		return nil, err
	}
	events, ok := envelope["events"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("GET %s: response has no events object", microvmEgress(id)+"/log")
	}
	if _, ok := events["data"].([]any); !ok {
		return nil, fmt.Errorf("GET %s: response has no events.data list", microvmEgress(id)+"/log")
	}
	return envelope, nil
}

// Outbound policy presets (user_api, bearer token).
//
//	GET /microvm/egress/presets
//	    -> {success, presets: [{id, version, source, name, description, rule_count,
//	        policy: {default_action, rules: [{action, target, protocol, ports, source}]}}]}
//
// Ready-made policy documents. A preset's policy is a complete document for
// SetMicrovmEgress or for egress_policy at create, as returned or edited.
// Applying one stores an ordinary policy: nothing links the sandbox to the
// preset, and a later change of the list never changes a stored policy.

// ListMicrovmEgressPresets returns the platform's outbound policy presets. A
// 2xx answer without a "presets" list is an error, never an empty catalog.
func (c *Client) ListMicrovmEgressPresets(ctx context.Context) ([]map[string]any, error) {
	const path = "/microvm/egress/presets"
	envelope, err := c.doItem(ctx, "GET", path, nil, "")
	if err != nil {
		return nil, err
	}
	list, ok := envelope["presets"].([]any)
	if !ok {
		return nil, fmt.Errorf("GET %s: response has no presets list", path)
	}
	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		preset, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GET %s: presets[%d] is not an object", path, i)
		}
		if _, ok := preset["policy"].(map[string]any); !ok {
			return nil, fmt.Errorf("GET %s: presets[%d] has no policy document", path, i)
		}
		out = append(out, preset)
	}
	return out, nil
}
