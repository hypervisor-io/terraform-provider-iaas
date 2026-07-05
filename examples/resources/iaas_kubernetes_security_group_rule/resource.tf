# A single firewall rule on one of a Kubernetes cluster's auto-provisioned
# security groups. The cluster auto-provisions up to three groups at
# create time (not themselves Terraform resources):
#   - "lb"     internet-facing apiserver ingress, attached to the CP load
#              balancer instance
#   - "cp"     control-plane node ingress
#   - "worker" worker node ingress
#
# There is NO update endpoint — changing any field replaces the rule
# (delete old + add new).

# Allow a specific office CIDR to reach the apiserver (scope = "lb").
resource "iaas_kubernetes_security_group_rule" "apiserver_office" {
  cluster_id = iaas_kubernetes_cluster.prod.id
  scope      = "lb"

  direction      = "ingress"
  protocol       = "tcp"
  port_range_min = 6443
  port_range_max = 6443
  ip_version     = "ipv4"
  cidr           = "203.0.113.0/24"
  description    = "Office access to the apiserver"
}

# Allow the NodePort range from anywhere (scope = "worker").
resource "iaas_kubernetes_security_group_rule" "nodeport_range" {
  cluster_id = iaas_kubernetes_cluster.prod.id
  scope      = "worker"

  direction      = "ingress"
  protocol       = "tcp"
  port_range_min = 30000
  port_range_max = 32767
  ip_version     = "ipv4"
  cidr           = "0.0.0.0/0"
  description    = "NodePort service range"
}

# Rules can also reference another security group or an IP set instead of a
# CIDR (cidr / remote_group_id / ip_set_id are mutually exclusive — exactly
# one must be set).
resource "iaas_kubernetes_security_group_rule" "from_bastion_sg" {
  cluster_id = iaas_kubernetes_cluster.prod.id
  scope      = "cp"

  direction       = "ingress"
  protocol        = "tcp"
  port_range_min  = 22
  port_range_max  = 22
  ip_version      = "ipv4"
  remote_group_id = iaas_security_group.bastion.id
  description     = "SSH from the bastion security group"
}

# ICMP/all-protocol rules omit the port range.
resource "iaas_kubernetes_security_group_rule" "icmp" {
  cluster_id = iaas_kubernetes_cluster.prod.id
  scope      = "worker"

  direction   = "ingress"
  protocol    = "icmp"
  ip_version  = "ipv4"
  cidr        = "10.0.0.0/8"
  description = "Internal ping"
}

# security_group_id (the resolved lb/cp/worker SG this rule belongs to) and
# internal (whether it is one of the cluster's own auto-provisioned rules,
# e.g. the default SSH/apiserver access rules) are read-only.
output "nodeport_rule_security_group_id" {
  value = iaas_kubernetes_security_group_rule.nodeport_range.security_group_id
}

# Import an existing rule with the 3-PART composite id
# "<cluster_id>/<scope>/<rule_id>":
#   terraform import iaas_kubernetes_security_group_rule.nodeport_range \
#     11111111-1111-1111-1111-111111111111/worker/44444444-4444-4444-4444-444444444444
