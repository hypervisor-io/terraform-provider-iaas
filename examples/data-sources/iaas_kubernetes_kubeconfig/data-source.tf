# Download the admin kubeconfig for a managed Kubernetes cluster. The server
# mints a FRESH cluster-admin client certificate on every read and embeds it
# inline (nothing is persisted), so the output is sensitive. The cluster must
# have reached the "running" state before a kubeconfig is available.

variable "cluster_id" {
  type = string
}

data "iaas_kubernetes_kubeconfig" "prod" {
  cluster_id = var.cluster_id
}

# Write the kubeconfig to a file for use with kubectl / helm.
resource "local_sensitive_file" "kubeconfig" {
  content  = data.iaas_kubernetes_kubeconfig.prod.kubeconfig
  filename = "${path.module}/kubeconfig.yaml"
}

# ...or feed it straight to the Kubernetes / Helm providers:
#
# provider "kubernetes" {
#   # Note: each read rotates the embedded client cert, so prefer writing to a
#   # file (above) when you need a stable credential across runs.
#   config_path = local_sensitive_file.kubeconfig.filename
# }
