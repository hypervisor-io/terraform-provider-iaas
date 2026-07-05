package client

import (
	"context"
	"fmt"
	"net/url"
)

// Kubernetes SSL certificate (CHILD of a cluster) endpoints, verified against
// the real UserApi Kubernetes\SslCertController + LoadBalancerService +
// routes/user_api.php (Gap G6). A cert secures the cluster's CP load balancer
// (fronting the apiserver with a stable domain instead of the bare LB IP). The
// cluster id is part of every path.
//
//	LIST    GET    /kubernetes/cluster/{clusterID}/ssl-certificates
//	                → {success,certs:[...]} (BARE array under "certs", NOT a
//	                  Laravel paginator). ONLY metadata is returned — the
//	                  index() query explicitly selects
//	                  [id,name,type,domain,san_domains,expires_at,
//	                  letsencrypt_status,letsencrypt_error,letsencrypt_domains,
//	                  created_at]. certificate/private_key/chain are NEVER
//	                  included, even on this endpoint's happy path (private_key
//	                  is additionally $hidden model-wide).
//	CREATE  POST   /kubernetes/cluster/{clusterID}/ssl-certificates
//	                body {source (req: "letsencrypt"|"custom"), domain (req),
//	                      name?, certificate? (req if source=custom),
//	                      private_key? (req if source=custom), chain?,
//	                      san_domains?, expires_at?}
//	                → 200 {success,message,certificate:{id,...}}
//	                [idempotency.user]
//	                Server-side: source=custom mass-assigns straight onto the
//	                LbCertificate row (type defaults to the DB enum "manual");
//	                source=letsencrypt ignores certificate/private_key/chain/
//	                name (name is forced to "LE: <domain>") and kicks off ACME
//	                (letsencrypt_status starts "pending_dns").
//	DELETE  DELETE /kubernetes/cluster/{clusterID}/ssl-certificate/{certID}
//	                (note: SINGULAR "ssl-certificate") → 200 {success,message}
//	                [idempotency.user]
//
// There is NO per-cert SHOW route and NO update route — every field is
// immutable (rotate by replacing). GetKubernetesSslCert therefore lists and
// matches by id, synthesising a 404 *APIError (IsNotFound) when absent.
//
// SENSITIVE / WRITE-ONLY: certificate, private_key and chain are never
// present in the LIST response (unlike the plain iaas_lb_certificate, whose
// LB-SHOW-embedded certificates[] DOES include certificate/chain). The
// resource must therefore echo ALL THREE from the plan and preserve them
// verbatim across reads — never overwrite them from an API object.
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateKubernetesSslCert adds a certificate to the cluster's CP load balancer.
// The cluster id is in the path; body carries the SslCertController's own
// input shape (source, domain, + conditional fields). The "certificate"
// envelope is unwrapped, returning the created object WITH its id. idemKey is
// sent as the Idempotency-Key header (a UUID is generated when empty).
func (c *Client) CreateKubernetesSslCert(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("CreateKubernetesSslCert: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/ssl-certificates",
		body, "certificate", map[string]string{"Idempotency-Key": idemKey})
}

// ListKubernetesSslCerts returns every certificate on the cluster's CP load
// balancer (empty list if the cluster has no CP LB yet). The index returns
// {"certs":[...]} — a BARE array under the named "certs" key (NOT a
// paginator) — so doItem(key="") fetches the bare envelope (surfacing C3
// success:false) and the "certs" array is flattened.
func (c *Client) ListKubernetesSslCerts(ctx context.Context, clusterID string) ([]map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("ListKubernetesSslCerts: empty cluster id")
	}
	top, err := c.doItem(ctx, "GET",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/ssl-certificates", nil, "")
	if err != nil {
		return nil, err
	}
	raw, _ := top["certs"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// GetKubernetesSslCert finds a single certificate by id via read-by-scan over
// the cluster's certificate LIST (there is NO per-cert SHOW route). A cert id
// absent from the list — or a non-2xx on the parent cluster's LIST — surfaces
// as an *APIError with Status 404 (recognised by IsNotFound), so the
// resource's Read removes the row from state and Terraform plans a recreate.
func (c *Client) GetKubernetesSslCert(ctx context.Context, clusterID, certID string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("GetKubernetesSslCert: empty cluster id")
	}
	if certID == "" {
		return nil, fmt.Errorf("GetKubernetesSslCert: empty certificate id")
	}
	certs, err := c.ListKubernetesSslCerts(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for _, cert := range certs {
		if id, _ := cert["id"].(string); id == certID {
			return cert, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "kubernetes cluster ssl certificate not found"}
}

// DeleteKubernetesSslCert removes a certificate from the cluster's CP load
// balancer. The route is a DELETE to the SINGULAR "ssl-certificate" path and
// carries idempotency.user, so — like DeleteKubernetesNodePool — this inlines
// doWithHeaders + responseError + decodeItem("") rather than doVoid (which has
// no header seam).
func (c *Client) DeleteKubernetesSslCert(ctx context.Context, clusterID, certID, idemKey string) error {
	if clusterID == "" {
		return fmt.Errorf("DeleteKubernetesSslCert: empty cluster id")
	}
	if certID == "" {
		return fmt.Errorf("DeleteKubernetesSslCert: empty certificate id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	path := "/kubernetes/cluster/" + url.PathEscape(clusterID) + "/ssl-certificate/" + url.PathEscape(certID)

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
