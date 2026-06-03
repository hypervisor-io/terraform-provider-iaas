# Resolve the UUID of an active Kubernetes version by its semantic version, for
# use as kubernetes_version_id when creating a cluster.

data "iaas_kubernetes_version" "v131" {
  name = "1.31.4"
}

output "kubernetes_version_id" {
  value = data.iaas_kubernetes_version.v131.id
}
