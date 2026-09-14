# A backend is a CHILD of a load balancer: it references the parent LB's id via
# load_balancer_id. A backend is a pool of target servers traffic is sent to.
resource "iaas_load_balancer" "example" {
  name                = "web-lb"
  lb_plan_id          = "44444444-4444-4444-4444-444444444444"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_lb_backend" "web" {
  # Parent load balancer id - part of the API path. Changing it forces a new resource.
  load_balancer_id = iaas_load_balancer.example.id

  name = "web-servers"

  # Load-balancing algorithm: "roundrobin" (default), "leastconn" or "source".
  # Updatable in place.
  algorithm = "roundrobin"

  # Proxy mode: "http" (default) or "tcp". Updatable in place.
  mode = "http"

  # Optional: time to wait for a backend server connection to establish, in
  # seconds (1-75). Omit for the load balancer default of 5 s.
  connect_timeout = 10

  # Optional: max time a backend server has to respond once connected, in
  # seconds (1-86400). Covers both a slow send and a slow read (HAProxy has
  # no separate read/send timeout). Omit to derive it from the idle_timeout
  # of the frontend(s) referencing this backend; an explicit value here
  # always wins over that derivation.
  server_timeout = 900
}

# Add target servers to the backend with iaas_lb_target.

# Import a backend with the COMPOSITE id "<load_balancer_id>/<backend_id>", e.g.:
#   terraform import iaas_lb_backend.web 11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222
