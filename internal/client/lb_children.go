package client

// Shared read-by-scan helpers for the load balancer CHILD resources (frontends,
// backends, targets, certificates, routing rules).
//
// None of the LB children have an individual SHOW route - they are all EMBEDDED
// in the parent load balancer SHOW payload:
//
//	load_balancer.frontends[]                  (each with routing_rules[])
//	load_balancer.backends[]                   (each with targets[])
//	load_balancer.certificates[]
//
// So every child's Get<Child> reads-by-scan: call GetLoadBalancer(lbId), then
// scan the relevant embedded array for the child id. These helpers centralise
// the (interface{} → []map[string]any) coercion the scan needs.

// lbChildren extracts a top-level embedded array (e.g. "frontends", "backends",
// "certificates") from a load_balancer SHOW object, coercing each element to a
// map. A missing/empty/malformed array yields nil.
func lbChildren(lb map[string]any, key string) []map[string]any {
	raw, ok := lb[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// lbNestedChild extracts a NESTED embedded array (e.g. a backend's "targets" or a
// frontend's "routing_rules") from a parent child object. It is the same coercion
// as lbChildren but reads from an arbitrary parent map rather than the LB root.
func lbNestedChild(parent map[string]any, key string) []map[string]any {
	return lbChildren(parent, key)
}
