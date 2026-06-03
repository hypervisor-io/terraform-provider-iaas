# =============================================================================
# Composed example stack: a public-facing web application.
#
# This stack wires the platform's resources into a single dependency graph to
# prove end-to-end resource binding:
#
#   data: location → plan → image
#                        │
#   iaas_ssh_key         │
#        │               ▼
#        │          iaas_vpc ──► iaas_vpc_subnet
#        │               │              │
#        │               └──────┬───────┘
#        ▼                      ▼
#   iaas_security_group ──► iaas_instance (×N, in the subnet, with the key)
#                                  │
#                                  ▼
#   iaas_load_balancer ──► iaas_lb_backend ──► iaas_lb_target (×N → instances)
#        │
#        └──────────────► iaas_lb_frontend (:80 → default backend)
#
# Every reference below is a real Terraform reference (e.g.
# `vpc_id = iaas_vpc.main.id`), so `tofu plan` builds the graph and applies the
# resources in dependency order.
# =============================================================================

provider "iaas" {
  # When the variables are left empty, these resolve to null and the provider
  # falls back to the IAAS_API_ENDPOINT / IAAS_API_TOKEN environment variables.
  endpoint = var.iaas_api_endpoint != "" ? var.iaas_api_endpoint : null
  token    = var.iaas_api_token != "" ? var.iaas_api_token : null
}

# ── Catalog lookups ──────────────────────────────────────────────────────────

# Deploy location (hypervisor group), resolved by slug / display name.
data "iaas_location" "this" {
  name = var.location_name
}

# Instance plan, resolved by name within the location.
data "iaas_plan" "web" {
  location_id = data.iaas_location.this.id
  name        = var.plan_name
  plan_group  = var.plan_group
}

# OS image, resolved by name and scoped to the location.
data "iaas_image" "os" {
  name                = var.image_name
  hypervisor_group_id = var.hypervisor_group_id
}

# ── SSH key injected into the instances at deploy time ───────────────────────

resource "iaas_ssh_key" "deploy" {
  name       = "web-app-deploy"
  public_key = var.ssh_public_key
}

# ── Private network: VPC + subnet ────────────────────────────────────────────

resource "iaas_vpc" "main" {
  name                = "webapp"
  cidr                = var.vpc_cidr
  hypervisor_group_id = var.hypervisor_group_id
  description         = "Web application private network"
}

resource "iaas_vpc_subnet" "web" {
  vpc_id = iaas_vpc.main.id
  cidr   = var.subnet_cidr
  type   = "public"
  name   = "web tier"
}

# ── Security group attached to the web instances ─────────────────────────────

resource "iaas_security_group" "web" {
  name        = "web-sg"
  description = "Public web servers"

  # Attach the group to every web instance by id.
  instance_ids = [for i in iaas_instance.web : i.id]

  rules = [
    # Allow inbound HTTP from anywhere.
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 80
      port_range_max = 80
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "HTTP"
    },
    # Allow inbound HTTPS from anywhere.
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 443
      port_range_max = 443
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "HTTPS"
    },
    # Allow inbound SSH from anywhere (tighten this to a bastion / IP set in
    # production).
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 22
      port_range_max = 22
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "SSH"
    },
  ]
}

# ── Web instances ────────────────────────────────────────────────────────────

resource "iaas_instance" "web" {
  count = var.web_instance_count

  # Immutable placement.
  location_id = data.iaas_location.this.id
  plan_id     = data.iaas_plan.web.id
  image_id    = data.iaas_image.os.id

  # Place each instance inside the VPC subnet.
  vpc_id        = iaas_vpc.main.id
  vpc_subnet_id = iaas_vpc_subnet.web.id

  # Write-only deploy fields.
  ssh_keys = [iaas_ssh_key.deploy.id]
  timezone = "UTC"

  # Updatable in place.
  hostname     = "web-${count.index + 1}"
  display_name = "Web ${count.index + 1}"

  timeouts {
    create = "30m"
    delete = "30m"
  }
}

# ── Load balancer fronting the web instances ─────────────────────────────────

resource "iaas_load_balancer" "web" {
  name                = "web-lb"
  lb_plan_id          = var.lb_plan_id
  hypervisor_group_id = var.hypervisor_group_id

  timeouts {
    create = "20m"
    delete = "20m"
  }
}

resource "iaas_lb_backend" "web" {
  load_balancer_id = iaas_load_balancer.web.id
  name             = "web-servers"
  algorithm        = "roundrobin"
  mode             = "http"
}

# One target per web instance. instance_id links the target to the instance for
# tracking; the routable address is target_ip:target_port (the instance's
# private subnet IP on the HTTP port).
resource "iaas_lb_target" "web" {
  count = var.web_instance_count

  load_balancer_id = iaas_load_balancer.web.id
  backend_id       = iaas_lb_backend.web.id

  instance_id = iaas_instance.web[count.index].id
  target_ip   = iaas_instance.web[count.index].primary_private_ip
  target_port = 80

  weight  = 100
  enabled = true
}

resource "iaas_lb_frontend" "http" {
  load_balancer_id = iaas_load_balancer.web.id

  name               = "http-in"
  port               = 80
  protocol           = "http"
  mode               = "http"
  default_backend_id = iaas_lb_backend.web.id
  enabled            = true
}
