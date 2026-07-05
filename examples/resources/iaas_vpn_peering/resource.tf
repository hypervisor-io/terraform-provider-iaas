# A VPN peering links TWO iaas_vpn_gateway resources OWNED BY THE SAME ACCOUNT,
# in DIFFERENT VPCs, over WireGuard. This is distinct from iaas_vpn_peer's
# "site_to_site" flavour, which connects to an arbitrary THIRD-PARTY endpoint
# you configure yourself (public_key/endpoint/allowed_ips). A peering only
# takes the remote gateway's id: the server derives everything else (a shared
# pre-shared key, each side's public_key/endpoint, tunnel IPs, and
# allowed_ips = the remote VPC's CIDR + tunnel subnet).
#
# Assumes two existing iaas_vpn_gateway resources, each in its own VPC. See
# examples/resources/iaas_vpn_gateway.
variable "vpc_a_gateway_id" {
  type = string
}

variable "vpc_b_gateway_id" {
  type = string
}

# Peer VPC A's gateway to VPC B's gateway. This creates ONE row (on the VPC A
# side); the server also creates the symmetric row on VPC B's gateway, but that
# row is NOT tracked by this resource.
resource "iaas_vpn_peering" "a_to_b" {
  vpn_gateway_id    = var.vpc_a_gateway_id
  remote_gateway_id = var.vpc_b_gateway_id
}

# To manage BOTH sides as Terraform-owned resources (so destroying either
# config removes its own row), declare the mirrored peering too, swapping
# vpn_gateway_id / remote_gateway_id:
resource "iaas_vpn_peering" "b_to_a" {
  vpn_gateway_id    = var.vpc_b_gateway_id
  remote_gateway_id = var.vpc_a_gateway_id
}

# There is no update route: vpn_gateway_id and remote_gateway_id are the only
# inputs, and both force replacement. Every other attribute (name, type,
# public_key, endpoint, tunnel_ip, allowed_ips, keepalive, enabled) is
# server-derived and read-only.
#
# The pre-shared key the server generates for the pairing is never returned by
# the API (encrypted + hidden), so it is not exposed by this resource at all.

# Import a VPN peering with the COMPOSITE id "<vpn_gateway_id>/<peering_id>", e.g.:
#   terraform import iaas_vpn_peering.a_to_b 11111111-1111-1111-1111-111111111111/88888888-8888-8888-8888-888888888888
