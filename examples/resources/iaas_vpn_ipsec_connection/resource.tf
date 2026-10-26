# An IPsec site-to-site connection is a CHILD of a VPN gateway: it joins the
# gateway's VPC to a remote network (an office router, a firewall, another cloud)
# over IKEv2 with a pre-shared key. The first connection on a gateway also
# installs the IPsec packages in the gateway, so the first apply takes longer.

# Assumes an existing, active iaas_vpn_gateway. See examples/resources/iaas_vpn_gateway.
variable "vpn_gateway_id" {
  type = string
}

# The pre-shared key is a secret: pass it in through a sensitive variable, never
# a literal. 16 to 128 printable ASCII characters, no spaces, double quotes or
# backslashes.
variable "office_psk" {
  type      = string
  sensitive = true
}

resource "iaas_vpn_ipsec_connection" "office" {
  # Parent gateway id - part of the API path. Changing this forces a new resource.
  vpn_gateway_id = var.vpn_gateway_id

  name           = "office-hq"
  remote_address = "198.51.100.7"

  # WRITE-ONLY and sensitive: stored encrypted by the platform and never returned,
  # so it cannot be read back or recovered on import. Changing it updates the key
  # in place; it does not replace the connection.
  psk = var.office_psk

  # Networks reachable through the tunnel. They must not overlap the VPC, the
  # tunnel subnet, WireGuard peers, VPC peerings or other IPsec connections.
  remote_ts = ["192.168.50.0/24"]

  # Everything below is optional. Defaults: the whole VPC range as local_ts,
  # IKEv2, the "modern" proposal preset, initiator role, dead-peer detection every
  # 30 seconds, UDP encapsulation on.
  # local_ts          = ["10.60.0.0/16"]
  # proposal_preset   = "modern"
  # role              = "initiator"
  # udp_encapsulation = true
}

# create and update wait until the gateway has applied the change
# (apply_state = "applied"); the live tunnel (sa_state) depends on the remote
# device and is not waited for. The values to enter on the other device are in
# remote_settings (a JSON document).
output "office_our_public_ip" {
  value = jsondecode(iaas_vpn_ipsec_connection.office.remote_settings).our_public_ip
}

output "office_tunnel_state" {
  value = iaas_vpn_ipsec_connection.office.sa_state
}

# A responder that waits for a peer which identifies itself by name (the peer may
# have a dynamic address):
#
# resource "iaas_vpn_ipsec_connection" "roadside" {
#   vpn_gateway_id = var.vpn_gateway_id
#   name           = "branch-dynamic"
#   role           = "responder"
#   remote_id_type = "fqdn"
#   remote_id      = "branch.example.com"
#   psk            = var.branch_psk
#   remote_ts      = ["192.168.60.0/24"]
# }

# Import with the COMPOSITE id "<gateway_id>/<connection_id>", e.g.:
#   terraform import iaas_vpn_ipsec_connection.office 22222222-2222-2222-2222-222222222222/88888888-8888-8888-8888-888888888888
# (psk cannot be recovered on import - set it in config; the next apply sends it.)
