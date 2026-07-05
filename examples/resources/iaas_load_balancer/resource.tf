# A load balancer (HAProxy) backed by a dedicated instance. Deploy it in PUBLIC
# mode (just a location), or in VPC mode (a VPC subnet). Creation is ASYNCHRONOUS:
# the backing instance is provisioned and this resource waits for the LB status
# to become "active" (deploying → configuring → active).
#
# This is the CORE load balancer only. Its frontends, backends, targets,
# certificates, and routing rules are managed by SEPARATE resources.

# ── PUBLIC mode: supply the location (hypervisor group) ──────────────────────
resource "iaas_load_balancer" "public" {
  name = "web-lb"

  # UUID of an enabled load balancer plan (sizes the backing instance).
  lb_plan_id = "44444444-4444-4444-4444-444444444444"

  # UUID of a load-balancer-enabled location. Required in public mode.
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

# ── VPC mode: supply a VPC + subnet instead of a location ────────────────────
resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_vpc_subnet" "public" {
  vpc_id = iaas_vpc.example.id
  cidr   = "10.0.1.0/24"
  type   = "public"
  name   = "lb tier"
}

resource "iaas_load_balancer" "vpc" {
  name       = "internal-lb"
  lb_plan_id = "44444444-4444-4444-4444-444444444444"

  # VPC mode: vpc_subnet_id is required when vpc_id is set. The location
  # (hypervisor_group_id) is derived from the VPC. A private subnet requires an
  # active NAT gateway for the LB to reach the internet.
  vpc_id        = iaas_vpc.example.id
  vpc_subnet_id = iaas_vpc_subnet.public.id
}

# Every input is IMMUTABLE - there is no update endpoint for the load balancer
# itself, so changing name, plan, vpc, or location forces a new resource. Tune
# the async wait via a timeouts block if needed:
#
#   resource "iaas_load_balancer" "public" {
#     # ...
#     timeouts {
#       create = "20m"
#       delete = "20m"
#     }
#   }

# The auto-assigned public IP, lifecycle status, and backing instance id are
# exported read-only:
output "lb_public_ip" {
  value = iaas_load_balancer.public.public_ip
}

output "lb_status" {
  value = iaas_load_balancer.public.status
}

# Import a load balancer by its UUID, e.g.:
#   terraform import iaas_load_balancer.public 11111111-1111-1111-1111-111111111111
