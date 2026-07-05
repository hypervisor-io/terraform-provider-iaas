# An instance may have AT MOST ONE VPC network interface attached at a time
# (enabling a second one fails server-side), so this is a STANDALONE resource
# keyed on instance_id rather than a nested block on iaas_instance.
#
# Enabling a VPC ALWAYS auto-assigns the LOWEST FREE address in vpc_subnet_id
# as the instance's first (primary) ip - there is no way to choose or omit
# it. That address is exposed read-only as auto_assigned_ip. Any further
# secondary addresses are managed via additional_ips (an order-independent
# set of free addresses drawn from the subnet's pool); primary_ip selects
# which attached address is primary.

resource "iaas_vpc" "example" {
  name                = "prodnet01"
  cidr                = "10.0.0.0/24"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

resource "iaas_vpc_subnet" "example" {
  vpc_id = iaas_vpc.example.id
  cidr   = "10.0.0.0/24"
  type   = "private"
  name   = "app tier"
}

resource "iaas_instance" "example" {
  location_id = "11111111-1111-1111-1111-111111111111"
  plan_id     = "22222222-2222-2222-2222-222222222222"
  image_id    = "33333333-3333-3333-3333-333333333333"
}

resource "iaas_instance_vpc_attachment" "example" {
  # Required. The instance to attach. Also this resource's unique key (an
  # instance has at most one VPC interface). Changing it forces a new
  # resource.
  instance_id = iaas_instance.example.id

  # Required. Changing either vpc_id or vpc_subnet_id forces a new resource
  # (disable, then re-enable) - there is no in-place "move to a different
  # VPC" operation.
  vpc_id        = iaas_vpc.example.id
  vpc_subnet_id = iaas_vpc_subnet.example.id

  # Optional. Extra addresses beyond the server auto-assigned
  # auto_assigned_ip, drawn from the subnet's FREE pool. Adding/removing an
  # address here attaches/detaches it in place. Do NOT list auto_assigned_ip
  # here - it is tracked separately, and the API itself refuses to remove an
  # instance's LAST vpc ip (destroy this resource instead to release
  # everything).
  additional_ips = ["10.0.0.10", "10.0.0.11"]

  # Optional. Which attached address (auto_assigned_ip or one of
  # additional_ips) should be primary. Defaults to the server auto-assigned
  # ip when omitted.
  primary_ip = "10.0.0.10"
}

# auto_assigned_ip and the full realized "ips" set are read-only:
output "instance_auto_assigned_ip" {
  value = iaas_instance_vpc_attachment.example.auto_assigned_ip
}

output "instance_vpc_ips" {
  value = iaas_instance_vpc_attachment.example.ips
}

# Import an existing attachment by instance_id (the attachment has no id of
# its own):
#   terraform import iaas_instance_vpc_attachment.example \
#     44444444-4444-4444-4444-444444444444
