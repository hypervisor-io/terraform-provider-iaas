# A DNS zone is an internal (per-VPC CoreDNS) DNS zone owned by your account. Once
# attached to one or more VPCs, its records become resolvable inside those VPCs.
# The zone name is immutable; only the description and the set of attached VPCs can
# change in place. Deletion is asynchronous (the provider waits for it to clear).

resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/16"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

resource "iaas_dns_zone" "example" {
  # Required, immutable. Lowercase alphanumeric with dots/hyphens, max 63 chars.
  # A bare public TLD ("com", "local", ...) is rejected to avoid shadowing real
  # DNS - use a compound internal name.
  name = "corp.internal"

  # Optional. The only scalar that can be changed in place.
  description = "Internal service discovery for production"

  # Optional set of VPC ids to attach the zone to. Add/remove ids to attach/detach
  # the zone from VPCs in place. Each VPC must belong to your account.
  vpc_ids = [iaas_vpc.example.id]
}
