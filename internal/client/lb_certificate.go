package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer CERTIFICATE endpoints (verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php).
//
// A certificate is a child of a load balancer. Unlike the other children there
// is NO PATCH/update route — a certificate is immutable (replace to rotate):
//
//	CREATE  POST   /load-balancer/{lbId}/certificates             body {name (req), certificate (req),
//	                  private_key (req), chain?}
//	                  → 200 {success,message,certificate:{id,...},sync}
//	DELETE  DELETE /load-balancer/{lbId}/certificate/{certId}     → 200 {success,message,sync}
//
// Let's Encrypt (POST .../le-certificate, POST .../certificate/{id}/retry) is
// DEFERRED for v1 — manual PEM upload only.
//
// SENSITIVE / WRITE-ONLY: private_key is in the model's $hidden, so it is NEVER
// returned by the LB SHOW. The resource echoes it from the plan and preserves it
// across reads (ImportStateVerifyIgnore). The certificate/chain PEM bodies ARE
// returned (decrypted) in the SHOW.
//
// Child writes are SYNCHRONOUS (syncConfig runs internally). There is NO
// individual certificate SHOW route — certificates are EMBEDDED in the LB SHOW
// under load_balancer.certificates[]. GetLBCertificate reads-by-scan.

// CreateLBCertificate uploads a manual PEM certificate to the given load
// balancer. name + certificate + private_key are required; chain optional. The
// "certificate" envelope is unwrapped, returning the created certificate WITH its
// id (private_key is NOT echoed back — it is $hidden).
func (c *Client) CreateLBCertificate(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBCertificate: empty lbID")
	}
	return c.doItem(ctx, "POST", "/load-balancer/"+url.PathEscape(lbID)+"/certificates", body, "certificate")
}

// DeleteLBCertificate deletes a certificate (and clears it from any frontend that
// referenced it). doVoid checks the success flag.
func (c *Client) DeleteLBCertificate(ctx context.Context, lbID, certificateID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBCertificate: empty lbID")
	}
	if certificateID == "" {
		return fmt.Errorf("DeleteLBCertificate: empty certificateID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/certificate/" + url.PathEscape(certificateID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBCertificate resolves a single certificate by scanning the parent load
// balancer's embedded certificates[] (there is NO individual certificate SHOW
// route). Returns a 404-shaped *APIError (IsNotFound) when absent. NOTE: the
// returned object will NOT contain private_key (it is $hidden) — the resource
// preserves the plan value for that field.
func (c *Client) GetLBCertificate(ctx context.Context, lbID, certificateID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBCertificate: empty lbID")
	}
	if certificateID == "" {
		return nil, fmt.Errorf("GetLBCertificate: empty certificateID")
	}
	lb, err := c.GetLoadBalancer(ctx, lbID)
	if err != nil {
		return nil, err
	}
	for _, cert := range lbChildren(lb, "certificates") {
		if id, ok := cert["id"].(string); ok && id == certificateID {
			return cert, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer certificate not found"}
}
