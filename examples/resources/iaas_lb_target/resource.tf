# A target is a member of a load balancer BACKEND (which is itself a child of a
# load balancer). Both the load_balancer_id and backend_id are part of the API
# path. A target is identified within its backend by (target_ip, target_port).
resource "iaas_load_balancer" "example" {
  name                = "web-lb"
  lb_plan_id          = "44444444-4444-4444-4444-444444444444"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_lb_backend" "web" {
  load_balancer_id = iaas_load_balancer.example.id
  name             = "web-servers"
}

resource "iaas_lb_target" "app1" {
  # Both parent ids are part of the API path. Changing either forces a new resource.
  load_balancer_id = iaas_load_balancer.example.id
  backend_id       = iaas_lb_backend.web.id

  # The (target_ip, target_port) pair is the backend-unique key. Changing either
  # forces a new resource.
  target_ip   = "10.0.0.5"
  target_port = 8080

  # Optional: link the target to an instance (tracking only — the API does NOT
  # derive target_ip from it). Changing it forces a new resource.
  # instance_id = "55555555-5555-5555-5555-555555555555"

  # Optional: relative weight (1-256, default 100) and enabled flag — both
  # updatable in place.
  weight  = 100
  enabled = true
}

# Import a target with the 3-PART COMPOSITE id
# "<load_balancer_id>/<backend_id>/<target_id>", e.g.:
#   terraform import iaas_lb_target.app1 11111111-.../22222222-.../33333333-...
