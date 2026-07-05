# A subnet is a CHILD of a VPC: it references the parent VPC's id via vpc_id.
resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

resource "iaas_vpc_subnet" "example" {
  # Parent VPC id - part of the API path. Changing this forces a new resource.
  vpc_id = iaas_vpc.example.id

  # IPv4 CIDR. The gateway and netmask are derived from it server-side.
  # Changing this forces a new resource.
  cidr = "192.168.10.0/24"

  # Optional. "public" (default) or "private". Immutable; changing it forces
  # a new resource.
  type = "public"

  # Optional. Defaults to a server-assigned "Subnet N". This is the only field
  # that can be changed in place.
  name = "web tier"
}

# Import a subnet with the COMPOSITE id "<vpc_id>/<subnet_id>", e.g.:
#   terraform import iaas_vpc_subnet.example 00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111
