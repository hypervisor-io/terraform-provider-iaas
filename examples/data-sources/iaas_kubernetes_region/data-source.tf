# Resolve the UUID of a region (hypervisor group) eligible to host a Kubernetes
# cluster, by its name or slug. Only regions with Kubernetes, VPC, AND Load
# Balancer features enabled are returned, so a match guarantees the region can
# host a cluster. Use the id as hypervisor_group_id on an iaas_kubernetes_cluster.

data "iaas_kubernetes_region" "nyc" {
  name = "nyc1"
}

output "hypervisor_group_id" {
  value = data.iaas_kubernetes_region.nyc.id
}
