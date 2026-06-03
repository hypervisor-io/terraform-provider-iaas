# A NAT gateway is a CHILD of a VPC: it gives the VPC's PRIVATE subnets outbound
# internet access. A VPC can have AT MOST ONE NAT gateway. The feature must be
# enabled for the VPC's location, and a public IP is auto-assigned at create.

resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

# A private subnet to route through the NAT gateway.
resource "iaas_vpc_subnet" "private" {
  vpc_id = iaas_vpc.example.id
  cidr   = "10.0.1.0/24"
  type   = "private"
  name   = "app tier"
}

resource "iaas_nat_gateway" "example" {
  # Parent VPC id — part of the API path. Changing this forces a new resource.
  vpc_id = iaas_vpc.example.id

  # Optional. Lowercase alphanumeric and dashes. Defaults to "natgw-<vpc name>".
  # Updatable in place.
  name = "natgw-prod"

  # Optional. Whether NAT (outbound translation) is active. Defaults to true.
  # Toggled in place (enable/disable).
  nat_enabled = true

  # Optional. The PRIVATE subnets attached to the gateway, as an order-independent
  # set. Adding/removing an id attaches/detaches that subnet in place. When omitted
  # the server attaches ALL of the VPC's private subnets and the resource adopts
  # that set into state. Only private subnets may be attached.
  subnet_ids = [iaas_vpc_subnet.private.id]
}

# Creation is ASYNCHRONOUS: the gateway is provisioned on a hypervisor and this
# resource waits for its status to become "active". Tune the wait via a timeouts
# block if needed:
#
#   resource "iaas_nat_gateway" "example" {
#     # ...
#     timeouts {
#       create = "20m"
#     }
#   }

# The auto-assigned public IP and lifecycle status are exported read-only:
output "nat_gateway_public_ip" {
  value = iaas_nat_gateway.example.public_ip
}

# Import a NAT gateway with the COMPOSITE id "<vpc_id>/<gateway_id>", e.g.:
#   terraform import iaas_nat_gateway.example 00000000-0000-0000-0000-000000000000/22222222-2222-2222-2222-222222222222
