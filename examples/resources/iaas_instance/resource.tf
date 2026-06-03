# A Cloud Service instance is created in two phases by the provider:
#   1. the instance record is created synchronously (location + plan), then
#   2. the OS image is deployed asynchronously; the provider waits on the
#      platform task until it completes before returning.
#
# The example wires the instance to catalog data sources (location, plan, image)
# and an SSH key resource so every required id is resolved by name.

# Deploy location (hypervisor group).
data "iaas_location" "nyc" {
  name = "nyc"
}

# Plan, looked up by name within the location.
data "iaas_plan" "small" {
  location_id = data.iaas_location.nyc.id
  name        = "s1.small"
}

# OS image, looked up by name (optionally scoped to a location).
data "iaas_image" "ubuntu" {
  name = "Ubuntu 24.04"
}

# An SSH key to inject at deploy time.
resource "iaas_ssh_key" "deploy" {
  name       = "deploy key"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILcl0K3kLJv6N4F2bWj2vJxQ1q3qY8m0Q3a2bC4d5E6F deploy@example.com"
}

resource "iaas_instance" "web" {
  # Immutable placement — changing any of these forces a new instance.
  location_id = data.iaas_location.nyc.id
  plan_id     = data.iaas_plan.small.id
  image_id    = data.iaas_image.ubuntu.id

  # Write-only deploy fields. The API does not return these on read, so they are
  # echoed from configuration and changing them forces a new instance.
  ssh_keys = [iaas_ssh_key.deploy.id]
  timezone = "UTC"

  # Optional cloud-init user-data (YAML).
  # cloudcfg = <<-EOT
  #   #cloud-config
  #   packages:
  #     - nginx
  # EOT

  # Updatable in place (PATCH) — NOT RequiresReplace.
  hostname     = "web-01"
  display_name = "Web 01"

  # Async create/delete may take a while; tune the waiter timeouts if needed.
  timeouts {
    create = "30m"
    delete = "30m"
  }
}

output "instance_public_ip" {
  value = iaas_instance.web.primary_public_ip
}
