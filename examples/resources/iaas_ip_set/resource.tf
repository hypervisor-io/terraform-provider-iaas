# An IP set is a named, version-scoped collection of CIDR entries that can be
# referenced from security group rules.
#
# The CIDR entries are managed inline as an order-independent set: adding or
# removing an entry from the `entries` set adds or removes it on the server in
# place, without replacing the IP set. Changing an entry's `comment` removes and
# re-adds that entry (the API has no entry-update endpoint).
#
# `ip_version` cannot be changed once the set has entries, so changing it forces
# a new resource.

resource "iaas_ip_set" "blocklist" {
  name        = "blocklist"
  description = "Known-bad source networks"
  ip_version  = "ipv4"

  entries = [
    {
      cidr    = "203.0.113.0/24"
      comment = "Abusive range"
    },
    {
      # A bare IP is normalised to /32 (IPv4) or /128 (IPv6) server-side.
      cidr = "198.51.100.7"
    },
    {
      cidr    = "192.0.2.0/24"
      comment = "Test network"
    },
  ]
}

# An IPv6 set works the same way; all entries must match the set's ip_version.
resource "iaas_ip_set" "ipv6_allowlist" {
  name       = "ipv6-allowlist"
  ip_version = "ipv6"

  entries = [
    {
      cidr    = "2001:db8::/32"
      comment = "Documentation prefix"
    },
  ]
}
