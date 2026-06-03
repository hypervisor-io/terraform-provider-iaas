# A certificate is a CHILD of a load balancer: a manually-uploaded SSL/TLS
# certificate (PEM cert + private key, optional chain). Certificates are
# IMMUTABLE — there is no update endpoint — so changing any field rotates
# (replaces) the certificate. (Let's Encrypt issuance is not managed here.)
resource "iaas_load_balancer" "example" {
  name                = "web-lb"
  lb_plan_id          = "44444444-4444-4444-4444-444444444444"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_lb_certificate" "example" {
  # Parent load balancer id — part of the API path. Changing it forces a new resource.
  load_balancer_id = iaas_load_balancer.example.id

  name        = "example-com"
  certificate = file("${path.module}/example.com.crt")

  # WRITE-ONLY and SENSITIVE: the private key is never returned by the API, so it
  # is taken from configuration and never refreshed. Keep it out of version
  # control (e.g. a variable or file outside the repo).
  private_key = file("${path.module}/example.com.key")

  # Optional intermediate chain.
  # chain = file("${path.module}/example.com.chain.crt")
}

# Attach the certificate to an https frontend via its ssl_certificate_id:
#
#   resource "iaas_lb_frontend" "https" {
#     load_balancer_id   = iaas_load_balancer.example.id
#     name               = "https-in"
#     port               = 443
#     protocol           = "https"
#     ssl_certificate_id = iaas_lb_certificate.example.id
#   }

# Import a certificate with the COMPOSITE id "<load_balancer_id>/<certificate_id>".
# NOTE: the private_key cannot be read back on import (it is write-only); set it in
# configuration after importing. e.g.:
#   terraform import iaas_lb_certificate.example 11111111-1111-1111-1111-111111111111/66666666-6666-6666-6666-666666666666
