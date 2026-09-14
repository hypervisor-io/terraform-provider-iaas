# A frontend is a CHILD of a load balancer: a listener (port + protocol) the
# load balancer accepts traffic on. The (port, protocol) pair must be unique per
# load balancer.
resource "iaas_load_balancer" "example" {
  name                = "web-lb"
  lb_plan_id          = "44444444-4444-4444-4444-444444444444"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_lb_backend" "web" {
  load_balancer_id = iaas_load_balancer.example.id
  name             = "web-servers"
}

resource "iaas_lb_frontend" "http" {
  # Parent load balancer id - part of the API path. Changing it forces a new resource.
  load_balancer_id = iaas_load_balancer.example.id

  name     = "http-in"
  port     = 80
  protocol = "http" # "http" (default), "https", "tcp" or "udp"
  mode     = "http" # "http" (default) or "tcp"

  # Optional: traffic with no matching routing rule goes to this default backend.
  default_backend_id = iaas_lb_backend.web.id

  # Optional: close idle connections after this many seconds (30-86400).
  # Omit for the load balancer default (50 s for http/https, 3600 s for tcp).
  idle_timeout = 3600

  # Optional: redirect HTTP to HTTPS with a 301 (only meaningful on an
  # http-mode listener on a port other than 443). Defaults to false.
  # ssl_redirect = true

  # For an https listener, attach one or more account certificates for SNI
  # (first is the default served when the client sends no matching SNI host):
  # protocol        = "https"
  # port            = 443
  # certificate_ids = [iaas_certificate.example.id]
  #
  # Or, for a single certificate, the legacy form still works:
  # ssl_certificate_id = iaas_certificate.example.id

  enabled = true
}

# All fields except the parent load_balancer_id are updatable in place.

# Import a frontend with the COMPOSITE id "<load_balancer_id>/<frontend_id>", e.g.:
#   terraform import iaas_lb_frontend.http 11111111-1111-1111-1111-111111111111/44444444-4444-4444-4444-444444444444
