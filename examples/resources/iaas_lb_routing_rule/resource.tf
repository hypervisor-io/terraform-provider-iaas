# A routing rule is an L7 rule on a load balancer FRONTEND: it sends matching
# traffic to a specific backend. Both the load_balancer_id and frontend_id are
# part of the API path.
resource "iaas_load_balancer" "example" {
  name                = "web-lb"
  lb_plan_id          = "44444444-4444-4444-4444-444444444444"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_lb_backend" "api" {
  load_balancer_id = iaas_load_balancer.example.id
  name             = "api-servers"
}

resource "iaas_lb_frontend" "http" {
  load_balancer_id = iaas_load_balancer.example.id
  name             = "http-in"
  port             = 80
  protocol         = "http"
}

resource "iaas_lb_routing_rule" "api" {
  # Both parent ids are part of the API path. Changing either forces a new resource.
  load_balancer_id = iaas_load_balancer.example.id
  frontend_id      = iaas_lb_frontend.http.id

  # The backend this rule routes matching traffic to (API field: lb_backend_id).
  backend_id = iaas_lb_backend.api.id

  # How match_value is compared: "path_prefix" (default), "path_exact", "host",
  # "header", "sni", "path_beg" or "hdr_host".
  match_type  = "path_prefix"
  match_value = "/api"

  # Optional host / header-name matchers and priority (lower wins; default 100).
  # match_host        = "api.example.com"
  # match_header_name = "X-Route"
  priority = 100

  enabled = true
}

# Import a routing rule with the 3-PART COMPOSITE id
# "<load_balancer_id>/<frontend_id>/<rule_id>", e.g.:
#   terraform import iaas_lb_routing_rule.api 11111111-.../44444444-.../55555555-...
