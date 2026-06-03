# Download the WireGuard CLIENT configuration for a road_warrior VPN peer. The
# rendered .conf uses a "[YOUR_PRIVATE_KEY]" placeholder for the client's own
# private key (the server never generates or stores it). The output is sensitive
# because it contains the gateway's public key and endpoint. Only works for peers
# of type "road_warrior".

# Assumes existing iaas_vpn_gateway + iaas_vpn_peer (road_warrior).
variable "vpn_gateway_id" {
  type = string
}

variable "vpn_peer_id" {
  type = string
}

data "iaas_vpn_peer_config" "laptop" {
  gateway_id = var.vpn_gateway_id
  peer_id    = var.vpn_peer_id
}

# Write the config to a local .conf file (remembering to substitute the client's
# real private key for the [YOUR_PRIVATE_KEY] placeholder before use).
resource "local_sensitive_file" "wg_conf" {
  content  = data.iaas_vpn_peer_config.laptop.config
  filename = "${path.module}/laptop.conf"
}
