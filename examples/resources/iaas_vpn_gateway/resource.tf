# A VPN gateway is a CHILD of a VPC: it is a WireGuard endpoint backed by a
# dedicated VM, deployed into one of the VPC's PUBLIC subnets, giving remote
# clients (road-warrior) and remote sites (site-to-site) encrypted access to the
# VPC's private networks. A VPC can have AT MOST ONE VPN gateway. The feature must
# be enabled for the VPC's location; a public IP is auto-assigned at create.

resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/16"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

# The gateway's backing VM is deployed into a PUBLIC subnet with a free IP.
resource "iaas_vpc_subnet" "public" {
  vpc_id = iaas_vpc.example.id
  cidr   = "10.0.0.0/24"
  type   = "public"
  name   = "edge"
}

resource "iaas_vpn_gateway" "example" {
  # Parent VPC id - part of the create API path. Changing this forces a new resource.
  vpc_id = iaas_vpc.example.id

  # Required. The VPN gateway plan (sizing/pricing of the backing VM).
  vpngw_plan_id = "11111111-1111-1111-1111-111111111111"

  # Required. The PUBLIC subnet to deploy the gateway's backing VM into. This is a
  # WRITE-ONLY input consumed at deploy time - it is NOT returned by the read
  # endpoint, so it is ignored on import (supply it in config).
  vpc_subnet_id = iaas_vpc_subnet.public.id

  # Optional. Display name (defaults to "vpngw-<random>" when omitted).
  name = "vpngw-prod"

  # Optional. WireGuard tunnel subnet (peer tunnel IPs are allocated from it; the
  # gateway takes .1). Defaults to "10.99.0.0/24". Must not overlap the VPC CIDR or
  # any VPC subnet.
  tunnel_subnet = "10.99.0.0/24"

  # Optional. WireGuard UDP listen port (1024-65535). Defaults to 51820.
  listen_port = 51820
}

# Creation is ASYNCHRONOUS: the backing VM is provisioned and this resource waits
# for the gateway's status to become "active" (a failed deploy ends in "error").
# Tune the wait via a timeouts block if needed:
#
#   resource "iaas_vpn_gateway" "example" {
#     # ...
#     timeouts {
#       create = "20m"
#     }
#   }

# The gateway's WireGuard public key and endpoint IPs are exported read-only.
# Peers use the public key + public_ip:listen_port to connect.
output "vpn_gateway_public_key" {
  value = iaas_vpn_gateway.example.public_key
}

output "vpn_gateway_endpoint" {
  value = "${iaas_vpn_gateway.example.public_ip}:${iaas_vpn_gateway.example.listen_port}"
}

# Import a VPN gateway with the COMPOSITE id "<vpc_id>/<gateway_id>", e.g.:
#   terraform import iaas_vpn_gateway.example 00000000-0000-0000-0000-000000000000/22222222-2222-2222-2222-222222222222
# (vpc_subnet_id is write-only and cannot be recovered on import - set it in config.)
