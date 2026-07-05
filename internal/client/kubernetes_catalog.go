package client

import (
	"context"
	"fmt"
	"net/url"
)

// Kubernetes kubeconfig / autoscaler-manifest + cluster-create catalog (search)
// endpoints, verified against the real UserApi Kubernetes\KubeconfigController +
// Kubernetes\SearchController + ClusterSearchService + routes/user_api.php. These
// back the id34 READ-ONLY data sources (kubeconfig, autoscaler-manifest, and the
// version/region/plan catalog lookups). No Master changes; thin client only.
//
// All routes live under the `/kubernetes` prefix. The kubeconfig + autoscaler
// routes are gated by the `kubernetes.kubeconfig_download` subuser permission and
// carry the `k8s.throttle:*` middleware (HTTP 429 on rate-limit → the shared
// transport already retries with backoff). The search routes are gated by
// `kubernetes.view` + `k8s.throttle:k8s_tasks`.
//
//	KUBECONFIG  GET /kubernetes/cluster/{id}/kubeconfig
//	    → RAW YAML (Content-Type application/yaml), NOT a JSON envelope. The
//	      controller mints a FRESH cluster-admin client certificate per call
//	      (CN=kubernetes-admin / O=system:masters) and embeds it inline — nothing
//	      is persisted. The body therefore carries live admin credentials and MUST
//	      be treated as SENSITIVE. Error paths ARE JSON: 404 {"error":"kubeconfig
//	      not yet available …"} when the cluster has not finished bootstrap (no
//	      ca_cert/ca_key yet); 500 {"error":"kubeconfig generation failed …"} on a
//	      mint error. doRaw returns the 2xx body verbatim and lets responseError
//	      parse the JSON error bodies normally.
//
//	AUTOSCALER  GET /kubernetes/cluster/{id}/autoscaler-manifest
//	    → RAW YAML (Content-Type text/yaml), NOT JSON. The rendered
//	      cluster-autoscaler manifest embeds a freshly-minted controller JWT
//	      base64-encoded inline as a Kubernetes Secret resource, so the body
//	      carries a live bearer credential and MUST be treated as SENSITIVE.
//	      Re-fetching ROTATES the active token (the autoscaler picks the new token
//	      up on its next reload). Error paths ARE JSON: 422 {"error":"autoscaling
//	      not enabled on this cluster"} / {"error":"cluster must be running"};
//	      500 {"error":"master domain not configured"}. doRaw handles both.
//
//	CATALOG     GET /kubernetes/search/{regions|versions|plans|cp-plans|lb-plans}
//	    → FLAT Select2 envelope {"results":[{id,text,…}],"pagination":{"more":…}}.
//	      Each result IS an item (NO "children" optgroup nesting) so decodeSelect2
//	      takes the flat path. The optional ?search= param is a server-side
//	      substring filter (name/slug for regions, semantic_version for versions,
//	      name for plans) — we still resolve the UNIQUE match client-side in the
//	      data source. Per-endpoint label/extra fields:
//	        regions  → text=region name; extra: slug, *_enabled flags.
//	        versions → text=semantic_version; extra: semantic_version.
//	        plans    → text="<name> - <cpu> CPU, <ram> MB, <storage> GB";
//	                   extra: name, cpu_cores, ram, storage, credit_value.
//	        cp-plans → IDENTICAL underlying instancePlans list as /plans (the
//	                   server splits the route only for semantic clarity).
//	        lb-plans → text="<name> - <cpu> CPU, <ram> MB"; extra: name,
//	                   cpu_cores, ram, credit_value (NO storage).
//
// An empty-id guard is applied on the cluster-id path argument (consistency);
// every id is url.PathEscape'd into the path.

// GetKubeconfig downloads a freshly-minted admin kubeconfig YAML for the cluster.
// The endpoint returns RAW YAML (application/yaml, NOT JSON), so this uses the
// raw transport (doRaw) rather than doItem. The returned text embeds a live
// cluster-admin client certificate minted per call (nothing is persisted
// server-side), so callers MUST treat it as sensitive. A cluster that has not
// finished bootstrapping yields 404 {"error":…} (parsed by responseError into an
// *APIError that IsNotFound recognises); a mint failure yields 500.
func (c *Client) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	if clusterID == "" {
		return "", fmt.Errorf("GetKubeconfig: empty clusterID")
	}
	return c.doRaw(ctx, "GET", "/kubernetes/cluster/"+url.PathEscape(clusterID)+"/kubeconfig")
}

// GetAutoscalerManifest renders the cluster-autoscaler manifest YAML the user
// applies with `kubectl apply -f -`. The endpoint returns RAW YAML (text/yaml,
// NOT JSON), so this uses doRaw. The manifest embeds a freshly-minted controller
// JWT (base64, inline Secret), so it carries a live bearer credential and MUST be
// treated as sensitive; re-fetching ROTATES the active token. It is gated to
// `running` clusters with worker autoscaling enabled — otherwise the controller
// returns 422 {"error":"autoscaling not enabled on this cluster"} /
// {"error":"cluster must be running"}, surfaced as an *APIError.
func (c *Client) GetAutoscalerManifest(ctx context.Context, clusterID string) (string, error) {
	if clusterID == "" {
		return "", fmt.Errorf("GetAutoscalerManifest: empty clusterID")
	}
	return c.doRaw(ctx, "GET", "/kubernetes/cluster/"+url.PathEscape(clusterID)+"/autoscaler-manifest")
}

// SearchK8sVersions lists active Kubernetes versions from the cluster-create
// catalog. The endpoint returns a FLAT Select2 envelope ({results:[{id,text,
// semantic_version}]}), which decodeSelect2 flattens. query is sent as the
// optional ?search= substring filter (matched server-side against
// semantic_version); pass "" to list all active versions. The text field carries
// the semantic version (e.g. "1.31.4").
func (c *Client) SearchK8sVersions(ctx context.Context, query string) ([]map[string]any, error) {
	return c.searchK8sSelect2(ctx, "/kubernetes/search/versions", query)
}

// SearchK8sRegions lists hypervisor groups eligible to host a Kubernetes cluster
// (kubernetes_enabled AND vpc_enabled AND lb_enabled). The endpoint returns a
// FLAT Select2 envelope ({results:[{id,text,slug,*_enabled}]}). query is the
// optional ?search= substring filter (matched against region name OR slug). The
// text field carries the region name; slug is also returned.
func (c *Client) SearchK8sRegions(ctx context.Context, query string) ([]map[string]any, error) {
	return c.searchK8sSelect2(ctx, "/kubernetes/search/regions", query)
}

// SearchK8sWorkerPlans lists enabled instance plans available for the WORKER pool
// picker (FLAT Select2). The underlying list is identical to the control-plane
// picker (the server splits /plans vs /cp-plans only for semantic clarity); the
// text field is "<name> - <cpu> CPU, <ram> MB, <storage> GB" and name/cpu_cores/
// ram/storage/credit_value are returned alongside.
func (c *Client) SearchK8sWorkerPlans(ctx context.Context, query string) ([]map[string]any, error) {
	return c.searchK8sSelect2(ctx, "/kubernetes/search/plans", query)
}

// SearchK8sControlPlanePlans lists enabled instance plans available for the
// CONTROL-PLANE picker (FLAT Select2). The underlying list is identical to
// SearchK8sWorkerPlans (same instancePlans source); the dedicated route exists
// for semantic clarity. Same row shape as the worker plans.
func (c *Client) SearchK8sControlPlanePlans(ctx context.Context, query string) ([]map[string]any, error) {
	return c.searchK8sSelect2(ctx, "/kubernetes/search/cp-plans", query)
}

// SearchK8sLoadBalancerPlans lists enabled LB plans available for the
// control-plane load-balancer picker (FLAT Select2). The text field is
// "<name> - <cpu> CPU, <ram> MB" and name/cpu_cores/ram/credit_value are returned
// (NO storage — LB plans have none).
func (c *Client) SearchK8sLoadBalancerPlans(ctx context.Context, query string) ([]map[string]any, error) {
	return c.searchK8sSelect2(ctx, "/kubernetes/search/lb-plans", query)
}

// SearchK8sVpcs lists VPCs the account owner can attach a Kubernetes cluster to,
// optionally constrained to one region (hypervisorGroupID) and/or filtered by a
// name/CIDR substring (query). Both filters are optional; pass "" to omit. Each
// FLAT Select2 row carries id, text ("name (cidr)"), name, cidr,
// hypervisor_group_id, has_nat_gateway and nat_public_ip.
func (c *Client) SearchK8sVpcs(ctx context.Context, hypervisorGroupID, query string) ([]map[string]any, error) {
	params := url.Values{}
	if hypervisorGroupID != "" {
		params.Set("hypervisor_group_id", hypervisorGroupID)
	}
	if query != "" {
		params.Set("search", query)
	}
	path := "/kubernetes/search/vpcs"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, raw, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeSelect2(raw)
}

// searchK8sSelect2 is the shared body of every k8s catalog search: it issues the
// GET with the optional ?search= filter and flattens the FLAT Select2 envelope
// via decodeSelect2. An empty query is omitted from the URL so the controller
// lists everything.
func (c *Client) searchK8sSelect2(ctx context.Context, path, query string) ([]map[string]any, error) {
	if query != "" {
		path += "?" + url.Values{"search": {query}}.Encode()
	}

	resp, raw, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeSelect2(raw)
}
