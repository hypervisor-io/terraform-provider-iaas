# Upload an account-level SSL/TLS certificate.
#
# Unlike iaas_lb_certificate, an iaas_certificate is NOT a child of a load
# balancer - it belongs to the account and can be attached to any of the
# account's load balancer frontends via ssl_certificate_id.
#
# There is no update endpoint, so changing name, certificate, private_key or
# chain forces a new resource (rotation). private_key and chain are
# write-only and sensitive: the API never returns them, so they are taken
# from configuration and never refreshed from the server.
#
# Let's Encrypt issuance is asynchronous (ACME) and is NOT modelled as a
# resource in v1 - use the panel or the MCP server's
# user.certificate.request_letsencrypt tool instead.
resource "iaas_certificate" "example" {
  name = "example-cert"

  certificate = file("${path.module}/example.crt")

  # WRITE-ONLY and SENSITIVE: the private key is never returned by the API, so it
  # is taken from configuration and never refreshed. Keep it out of version
  # control (e.g. a variable or file outside the repo).
  private_key = file("${path.module}/example.key")

  # Optional intermediate chain.
  # chain = file("${path.module}/example-chain.crt")
}

# Attach the certificate to any account load balancer's https frontend via
# its ssl_certificate_id:
#
#   resource "iaas_lb_frontend" "https" {
#     load_balancer_id   = iaas_load_balancer.example.id
#     name               = "https-in"
#     port               = 443
#     protocol           = "https"
#     ssl_certificate_id = iaas_certificate.example.id
#   }

# Computed attributes: domain, SAN domains, fingerprint and status refresh on
# every plan; changing any configured field forces a new resource.
output "certificate_domain" {
  value = iaas_certificate.example.domain
}

output "certificate_san_domains" {
  value = iaas_certificate.example.san_domains
}

output "certificate_fingerprint" {
  value = iaas_certificate.example.fingerprint_sha256
}

output "certificate_status" {
  value = iaas_certificate.example.status
}

# Import an existing certificate by its UUID. NOTE: private_key/chain cannot be
# read back on import (write-only); set them in configuration after importing.
#   terraform import iaas_certificate.example 88888888-8888-8888-8888-888888888888
