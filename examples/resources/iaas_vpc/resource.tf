resource "iaas_vpc" "example" {
  # Lowercase letters and digits only, max 16 chars. Changing this forces a new resource.
  name = "prodnet01"

  # CIDR block, must be within an RFC1918 private range. Changing this forces a new resource.
  cidr = "10.0.0.0/24"

  # UUID of a VPC-enabled hypervisor group (location). Changing this forces a new resource.
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"

  # Optional. The VPC API has no update endpoint, so changing this forces a new resource.
  description = "Production private network"
}
