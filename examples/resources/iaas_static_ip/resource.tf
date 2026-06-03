# Reserve a static IP in a specific location.
#
# Prerequisites:
#   1. Billing must be enabled on the platform (Cloud Service credits).
#   2. The chosen location must have static_ip_enabled = true.
#   3. ip_id must be a free, non-reserved public IPv4 UUID from that location's
#      available pool (use the /static-ips/available endpoint or the panel UI).
#
# Changing ip_id or hypervisor_group_id forces a new resource (deallocate + re-allocate).
# Deallocating an IP that is currently attached to an instance will fail — detach it first.

resource "iaas_static_ip" "example" {
  # UUID of the specific IP address to reserve from the location's available pool.
  # Obtain eligible ids via the panel's Static IPs → Available page or the API.
  # Changing this forces a new resource.
  ip_id = "00000000-0000-0000-0000-000000000001"

  # UUID of the hypervisor group (location) that owns this IP.
  # The location must have static IP support enabled.
  # Changing this forces a new resource.
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

# The allocated IPv4 address is available as a computed attribute.
output "static_ip_address" {
  value = iaas_static_ip.example.address
}

# The current attachment status ("allocated" or "attached") is refreshed on every plan.
output "static_ip_status" {
  value = iaas_static_ip.example.status
}
