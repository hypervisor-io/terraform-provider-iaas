# A TLS certificate on a Kubernetes cluster's CP load balancer. A cert is a
# CHILD of a cluster (its cluster_id is part of the API path, so changing it
# forces a new resource). There is NO update endpoint - every field is
# IMMUTABLE, so changing any of them rotates (replaces) the certificate.
#
# Once an active certificate exists, the cluster's Kubeconfig download
# endpoint rewrites `server:` to use the certificate's domain instead of the
# bare LB IP.

# ── source = "custom": manual PEM upload ──────────────────────────────────
resource "iaas_kubernetes_ssl_certificate" "custom" {
  # Parent cluster id - part of the API path. Changing it forces a new resource.
  cluster_id = iaas_kubernetes_cluster.prod.id

  source = "custom"
  domain = "api.example.com"

  certificate = file("${path.module}/api.example.com.crt")

  # WRITE-ONLY and SENSITIVE: the cluster ssl-certificates list never returns
  # certificate/private_key/chain (unlike iaas_lb_certificate's LB-SHOW embed,
  # which does return certificate/chain), so all three are taken from
  # configuration and never refreshed. Keep them out of version control (e.g.
  # a variable or file outside the repo).
  private_key = file("${path.module}/api.example.com.key")

  # Optional intermediate chain.
  # chain = file("${path.module}/api.example.com.chain.crt")

  # Optional comma-separated SAN domains and expiry.
  # san_domains = "api2.example.com,api3.example.com"
  # expires_at  = "2027-01-01T00:00:00Z"
}

# ── source = "letsencrypt": ACME issuance ──────────────────────────────────
resource "iaas_kubernetes_ssl_certificate" "le" {
  cluster_id = iaas_kubernetes_cluster.prod.id

  source = "letsencrypt"
  domain = "api.example.com"

  # certificate/private_key/chain are ignored for source = "letsencrypt" (the
  # server issues via ACME instead). Progress surfaces on letsencrypt_status
  # ("pending_dns" -> "active"/"error") - this resource does NOT poll/wait for
  # issuance to complete; re-run `terraform plan`/`refresh` to observe it.
}

output "le_status" {
  value = iaas_kubernetes_ssl_certificate.le.letsencrypt_status
}

# Import an existing certificate with the COMPOSITE id "<cluster_id>/<certificate_id>".
# NOTE: certificate/private_key/chain cannot be read back on import (write-only);
# set them in configuration after importing. e.g.:
#   terraform import iaas_kubernetes_ssl_certificate.custom 11111111-1111-1111-1111-111111111111/77777777-7777-7777-7777-777777777777
