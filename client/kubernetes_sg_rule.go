package client

import (
	"context"
	"fmt"
	"net/url"
)

// Kubernetes cluster security-group rule (CHILD of a cluster+scope) endpoints,
// verified against the real UserApi\Kubernetes\SecurityGroupController +
// SecurityGroupService + routes/user_api.php (Gap G7). Every cluster
// auto-provisions up to three security groups at create time - "lb" (internet-
// facing apiserver ingress, attached to the CP load balancer instance), "cp"
// (control-plane node ingress, attached to CPs) and "worker" (worker node
// ingress, attached to workers) - and this resource lets Terraform add/remove
// individual firewall rules on one of them without touching the standalone SG
// admin surface (iaas_security_group).
//
//	LIST    GET    /kubernetes/cluster/{clusterID}/security-group/{scope}
//	                → {success, rules:[...], security_group:{id,name}|null}.
//	                security_group is null (rules always []) when the scope was
//	                never provisioned on the cluster (e.g. legacy clusters
//	                predating the worker SG) - NOT a 404. `where(['scope' =>
//	                'lb|cp|worker'])` backs the route; an out-of-set scope 404s
//	                at the router level before the controller runs.
//	CREATE  POST   /kubernetes/cluster/{clusterID}/security-group/{scope}
//	                body {direction (req: ingress|egress),
//	                      protocol (req: tcp|udp|icmp|icmpv6|all|any),
//	                      port_range_min?, port_range_max?,
//	                      ip_version (req: ipv4|ipv6),
//	                      cidr?, remote_group_id?, ip_set_id?, description?}
//	                → 200 {success,message,rule:{id,...}} [idempotency.user].
//	                422 "invalid scope"; 404 "security group not provisioned
//	                for this scope" (valid scope, but the cluster has no SG
//	                for it yet - unlike LIST, create cannot synthesize a
//	                target group). Either cidr, remote_group_id or ip_set_id
//	                must be supplied (mutually exclusive; enforced by
//	                SecurityGroupService::addRule, not the FormRequest).
//	DELETE  DELETE /kubernetes/cluster/{clusterID}/security-group/{scope}/rule/{ruleID}
//	                → 200 {success,message} [idempotency.user]. 422 "rule does
//	                not belong to this scope" if ruleID's security_group_id !=
//	                the resolved scope SG's id (cross-scope deletion guard).
//
// There is NO per-rule SHOW route and NO update route (add-only; any field
// change is delete+add) - GetKubernetesClusterSgRule therefore lists rules for
// (cluster,scope) and matches by id, synthesising a 404 *APIError (IsNotFound)
// when absent, mirroring GetKubernetesSslCert / user_script.go.
//
// DEVIATION vs the standalone iaas_security_group rule shape: the cluster
// StoreClusterSgRuleRequest validates protocol against
// "tcp,udp,icmp,icmpv6,all,any" (an extra "any" not accepted by the standalone
// SecurityGroupController's rule validation). Note, however, that the
// `security_group_rules.protocol` DB column is a MySQL ENUM('tcp','udp','icmp',
// 'icmpv6','all') with no 'any' member (see migration
// 2026_03_04_000001_create_security_groups_tables.php) and no later migration
// widens it - so protocol="any" passes FormRequest validation but fails at the
// SecurityGroupRule::create() INSERT, which storeRule's catch(\Throwable)
// surfaces as a 422 with the raw DB error message. This resource still allows
// "any" in its schema validator (mirroring the FormRequest contract per the
// recipe's "controller wins" rule) rather than silently narrowing it; the
// 422 the API returns for "any" is a Master-side bug, not a provider bug.
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateKubernetesClusterSgRule adds a single rule to the cluster's scope
// security group. idemKey is sent as the Idempotency-Key header (a UUID is
// generated when empty). The "rule" envelope is unwrapped, returning the
// created object with its id.
func (c *Client) CreateKubernetesClusterSgRule(ctx context.Context, clusterID, scope string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("CreateKubernetesClusterSgRule: empty cluster id")
	}
	if scope == "" {
		return nil, fmt.Errorf("CreateKubernetesClusterSgRule: empty scope")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	path := "/kubernetes/cluster/" + url.PathEscape(clusterID) + "/security-group/" + url.PathEscape(scope)
	return c.doItemWithHeaders(ctx, "POST", path, body, "rule", map[string]string{"Idempotency-Key": idemKey})
}

// ListKubernetesClusterSgRulesEnvelope returns the full LIST envelope - both the
// "rules" array and the "security_group" {id,name} object the rules belong to
// (null when the scope has no SG provisioned on this cluster yet). Callers that
// only need the rules should use ListKubernetesClusterSgRules.
func (c *Client) ListKubernetesClusterSgRulesEnvelope(ctx context.Context, clusterID, scope string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("ListKubernetesClusterSgRulesEnvelope: empty cluster id")
	}
	if scope == "" {
		return nil, fmt.Errorf("ListKubernetesClusterSgRulesEnvelope: empty scope")
	}
	path := "/kubernetes/cluster/" + url.PathEscape(clusterID) + "/security-group/" + url.PathEscape(scope)
	// key="" → bare envelope (rules + security_group live side by side at the
	// top level; there is no single-object wrapper here).
	return c.doItem(ctx, "GET", path, nil, "")
}

// ListKubernetesClusterSgRules returns just the "rules" array for
// (cluster,scope) - an empty (not error) slice when the scope has no SG
// provisioned on this cluster yet.
func (c *Client) ListKubernetesClusterSgRules(ctx context.Context, clusterID, scope string) ([]map[string]any, error) {
	env, err := c.ListKubernetesClusterSgRulesEnvelope(ctx, clusterID, scope)
	if err != nil {
		return nil, err
	}
	raw, _ := env["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// GetKubernetesClusterSgRule finds a single rule by id via read-by-scan over the
// (cluster,scope) rule LIST (there is no per-rule SHOW route). A rule id absent
// from the list - or a non-2xx on the parent LIST - surfaces as an *APIError
// with Status 404 (recognised by IsNotFound), so the resource's Read removes
// the row from state and Terraform plans a recreate. The rule object already
// carries its own "security_group_id" column (SecurityGroupRule is $guarded=[]
// so every column, including security_group_id, serialises verbatim) - no
// synthetic augmentation from the envelope's "security_group" is needed.
func (c *Client) GetKubernetesClusterSgRule(ctx context.Context, clusterID, scope, ruleID string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("GetKubernetesClusterSgRule: empty cluster id")
	}
	if scope == "" {
		return nil, fmt.Errorf("GetKubernetesClusterSgRule: empty scope")
	}
	if ruleID == "" {
		return nil, fmt.Errorf("GetKubernetesClusterSgRule: empty rule id")
	}
	rules, err := c.ListKubernetesClusterSgRules(ctx, clusterID, scope)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if id, _ := rule["id"].(string); id == ruleID {
			return rule, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "kubernetes cluster security group rule not found"}
}

// DeleteKubernetesClusterSgRule removes a rule from the cluster's scope security
// group. The route carries idempotency.user, so - like DeleteKubernetesSslCert
// - this inlines doWithHeaders + responseError + decodeItem("") rather than
// doVoid (which has no header seam).
func (c *Client) DeleteKubernetesClusterSgRule(ctx context.Context, clusterID, scope, ruleID, idemKey string) error {
	if clusterID == "" {
		return fmt.Errorf("DeleteKubernetesClusterSgRule: empty cluster id")
	}
	if scope == "" {
		return fmt.Errorf("DeleteKubernetesClusterSgRule: empty scope")
	}
	if ruleID == "" {
		return fmt.Errorf("DeleteKubernetesClusterSgRule: empty rule id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	path := "/kubernetes/cluster/" + url.PathEscape(clusterID) + "/security-group/" + url.PathEscape(scope) + "/rule/" + url.PathEscape(ruleID)

	resp, raw, err := c.doWithHeaders(ctx, "DELETE", path, nil, map[string]string{
		"Idempotency-Key": idemKey,
	})
	if err != nil {
		return err
	}
	if err := responseError(resp, raw); err != nil {
		return err
	}
	_, err = decodeItem(raw, "")
	return err
}
