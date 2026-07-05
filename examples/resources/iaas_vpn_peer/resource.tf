# A VPN peer is a CHILD of a VPN gateway: it represents a remote client
# (road_warrior) or a remote network (site_to_site) allowed to connect through the
# gateway. Most fields are updatable in place; the type, tunnel_ip and dns are
# fixed at creation.

# Assumes an existing iaas_vpn_gateway. See examples/resources/iaas_vpn_gateway.
variable "vpn_gateway_id" {
  type = string
}

# A road-warrior peer: a single remote client device. Generate the client's
# WireGuard keypair on the device; supply its PUBLIC key here. The downloadable
# client config (for the .conf file) is available via the iaas_vpn_peer_config
# data source.
resource "iaas_vpn_peer" "laptop" {
  # Parent gateway id - part of the API path. Changing this forces a new resource.
  vpn_gateway_id = var.vpn_gateway_id

  type = "road_warrior"
  name = "my-laptop"

  # The client device's WireGuard PUBLIC key (NOT a secret).
  public_key = "Y2xpZW50cHVibGlja2V5QmFzZTY0RW5jb2RlZD0="

  # Optional. An extra symmetric pre-shared key. WRITE-ONLY and sensitive: it is
  # stored encrypted server-side and never returned, so it is preserved from
  # config and cannot be recovered on import.
  preshared_key = "cHJlc2hhcmVka2V5QmFzZTY0RW5jb2RlZD0="

  # tunnel_ip is auto-allocated from the gateway's tunnel_subnet when omitted.
  # allowed_ips defaults to the peer's tunnel_ip/32.
  # keepalive defaults to 25; enabled defaults to true.
}

# A site-to-site peer: a remote network the gateway dials out to.
resource "iaas_vpn_peer" "branch_office" {
  vpn_gateway_id = var.vpn_gateway_id

  type       = "site_to_site"
  name       = "branch-office"
  public_key = "cmVtb3Rlc2l0ZXB1YmtleUJhc2U2NEVuY29kZWQ9"

  # The remote endpoint the gateway connects to (host:port).
  endpoint = "203.0.113.10:51820"

  # The remote network(s) routed to this peer through the tunnel.
  allowed_ips = ["192.168.50.0/24"]

  keepalive = 25
}

# Import a VPN peer with the COMPOSITE id "<gateway_id>/<peer_id>", e.g.:
#   terraform import iaas_vpn_peer.laptop 22222222-2222-2222-2222-222222222222/77777777-7777-7777-7777-777777777777
# (preshared_key is write-only and cannot be recovered on import - set it in config.)
