# A security group is a named collection of stateful firewall rules that can be
# attached to instances.
#
# The rules are managed inline as an order-independent set: adding or removing a
# rule from the `rules` set adds or removes it on the server in place, without
# replacing the security group. Because the API has no rule-update endpoint,
# changing any field of an existing rule removes and re-adds that rule.
#
# Instances are attached via the `instance_ids` set: adding or removing an id
# attaches or detaches the group on that instance in place. An instance may have
# at most 10 security groups.

resource "iaas_security_group" "web" {
  name        = "web-sg"
  description = "Public web servers"

  # Attach this group to one or more instances by id.
  instance_ids = [
    iaas_instance.web1.id,
    iaas_instance.web2.id,
  ]

  rules = [
    # Allow inbound HTTP from anywhere.
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 80
      port_range_max = 80
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "HTTP"
    },
    # Allow inbound HTTPS from anywhere.
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 443
      port_range_max = 443
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "HTTPS"
    },
    # Allow inbound ICMP (ping) from anywhere — no ports for icmp.
    {
      direction  = "ingress"
      protocol   = "icmp"
      ip_version = "ipv4"
      cidr       = "0.0.0.0/0"
    },
    # Allow SSH only from a managed IP set (mutually exclusive with cidr).
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 22
      port_range_max = 22
      ip_version     = "ipv4"
      ip_set_id      = iaas_ip_set.office.id
      description    = "SSH from office"
    },
    # Allow inbound from another security group (e.g. a load-balancer SG).
    {
      direction       = "ingress"
      protocol        = "tcp"
      port_range_min  = 8080
      port_range_max  = 8080
      ip_version      = "ipv4"
      remote_group_id = iaas_security_group.lb.id
      description     = "App port from LB SG"
    },
  ]
}
