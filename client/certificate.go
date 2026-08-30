package client

import (
	"context"
	"fmt"
	"net/url"
)

// Certificate endpoints (Task 7 API contract; user_api, bearer token).
// Certificates are ACCOUNT-level - unlike iaas_lb_certificate they are NOT
// children of a load balancer; a manual PEM upload or a Let's Encrypt
// issuance produces a certificate that can then be attached to any of the
// account's load balancer frontends via ssl_certificate_id.
//
//	INDEX       GET    /certificates              → {success,certificates:[...]}
//	SHOW        GET    /certificate/{id}           → {success,certificate:{...,usages:[...]}}
//	USAGES      GET    /certificate/{id}/usages    → {success,usages:[...]} - folded into SHOW;
//	                                                  no dedicated client method, GetCertificate covers it
//	CREATE      POST   /certificates               body {name,certificate,private_key,chain?}
//	                                                → {success,certificate:{...}}
//	LETSENCRYPT POST   /certificates/letsencrypt    body {name?,domains:[...],via_load_balancer_id}
//	                                                → {success,certificate:{...}}
//	RETRY       POST   /certificate/{id}/retry      → {success,message}
//	DELETE      DELETE /certificate/{id}            → {success,message}; success:false at HTTP 200
//	                                                  on refusal (e.g. still in use by a frontend)
//
// SENSITIVE / WRITE-ONLY: certificate, private_key and chain PEM bodies are
// NEVER returned by any of these responses (unlike the load-balancer
// certificates[] embed, which does echo certificate/chain). Fields returned:
// id, name, domain, san_domains, type, status, letsencrypt_status,
// letsencrypt_error, renewal_blocked_reason, expires_at, fingerprint_sha256,
// issued_via_load_balancer_id, usage_count, created_at.

// ListCertificates returns every account certificate.
func (c *Client) ListCertificates(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/certificates", "certificates")
}

// GetCertificate fetches a single certificate by UUID, including its
// usages[] (the load balancer frontends that reference it via
// ssl_certificate_id). A missing id surfaces as a 404 *APIError
// (IsNotFound = true), so Read can RemoveResource.
func (c *Client) GetCertificate(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetCertificate: empty id")
	}
	return c.doItem(ctx, "GET", "/certificate/"+url.PathEscape(id), nil, "certificate")
}

// CreateCertificate uploads a manual PEM certificate. body must contain
// "name", "certificate" and "private_key"; "chain" is optional.
func (c *Client) CreateCertificate(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/certificates", body, "certificate")
}

// RequestLetsEncryptCertificate requests ACME issuance. body must contain
// "domains" ([]string) and "via_load_balancer_id" (the load balancer whose
// public IP serves the HTTP-01 challenge); "name" is optional. Issuance is
// asynchronous - the returned certificate starts in a pending/issuing state.
func (c *Client) RequestLetsEncryptCertificate(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/certificates/letsencrypt", body, "certificate")
}

// RetryCertificate re-attempts a failed Let's Encrypt issuance/renewal.
// The response carries only {success,message}; doVoid checks the flag.
func (c *Client) RetryCertificate(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("RetryCertificate: empty id")
	}
	return c.doVoid(ctx, "POST", "/certificate/"+url.PathEscape(id)+"/retry", nil)
}

// DeleteCertificate deletes an account certificate. A failure (e.g. still in
// use by a load balancer frontend) is signalled with success:false at
// HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteCertificate(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteCertificate: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/certificate/"+url.PathEscape(id), nil)
}
