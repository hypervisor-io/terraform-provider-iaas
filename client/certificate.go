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
//	UPDATE      PUT    /certificate/{id}            body {certificate,private_key,chain?,name?}
//	                                                → {success,certificate:{...},resync:[{id,name,success,error}]}
//	                                                  (NUI-V-R17-ACM-ROTATE1/2, Master 5167436db - replaces
//	                                                  the certificate material IN PLACE, same id, and
//	                                                  best-effort re-syncs every load balancer that
//	                                                  references it; 422 {success:false,message} - NO
//	                                                  machine-readable "code" field, unlike the Kubernetes
//	                                                  upgrade endpoints below - for: the certificate is not
//	                                                  a manual upload (Let's Encrypt renews automatically),
//	                                                  a key/certificate mismatch, an expired certificate, or
//	                                                  a chain that doesn't match the leaf; 404 cross-tenant
//	                                                  or not found)
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
//
// UPDATE uses the SAME singular /certificate/{id} path as SHOW/RETRY/DELETE -
// only INDEX/CREATE/LETSENCRYPT are plural /certificates[/…]. An earlier
// draft of this contract had UPDATE on the plural path; Master
// 5167436db is the landed, authoritative shape.

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

// UpdateCertificate replaces a manually-uploaded certificate's material in
// place (rotation without changing the id) via PUT /certificate/{id} (NOT
// /certificates/{id} - see the package doc comment). body must contain
// "certificate" and "private_key"; "chain" and "name" are optional. Returns
// the refreshed certificate object and the "resync" list describing, per
// load balancer that references this certificate, whether the server-side
// config re-sync succeeded ({"id","name","success","error"}); resync is nil
// when the response omits the key or it is not a JSON array.
//
// Rejections come back as a plain 422 {"success":false,"message":"..."} -
// there is NO machine-readable "code" field on this endpoint (unlike the
// Kubernetes upgrade endpoints), so a Let's Encrypt-issued certificate, a
// key/certificate mismatch, an expired certificate and a mismatched chain
// are only distinguishable by *APIError.Message text. Callers that need a
// dedicated diagnostic for the Let's Encrypt case should match on Message
// (case-insensitively contains "let's encrypt" - see
// internal/resources/certificate.go's Update).
func (c *Client) UpdateCertificate(ctx context.Context, id string, body map[string]any) (map[string]any, []map[string]any, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("UpdateCertificate: empty id")
	}
	env, err := c.doItem(ctx, "PUT", "/certificate/"+url.PathEscape(id), body, "")
	if err != nil {
		return nil, nil, err
	}
	cert, ok := env["certificate"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("UpdateCertificate: response missing %q object", "certificate")
	}

	var resync []map[string]any
	if raw, ok := env["resync"].([]any); ok {
		resync = make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				resync = append(resync, m)
			}
		}
	}

	return cert, resync, nil
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
