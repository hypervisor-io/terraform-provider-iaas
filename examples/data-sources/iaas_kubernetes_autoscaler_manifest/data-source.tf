# Render the cluster-autoscaler manifest for a managed Kubernetes cluster and
# apply it to the cluster. The manifest embeds a freshly-minted controller JWT
# (a bearer credential) inline as a Secret, so the output is sensitive; every
# read rotates the active token. The cluster must be "running" with worker
# autoscaling enabled.

variable "cluster_id" {
  type = string
}

data "iaas_kubernetes_autoscaler_manifest" "prod" {
  cluster_id = var.cluster_id
}

# Apply the manifest with the kubectl provider (or write it to a file and run
# `kubectl apply -f -` out of band).
resource "local_sensitive_file" "autoscaler" {
  content  = data.iaas_kubernetes_autoscaler_manifest.prod.manifest
  filename = "${path.module}/cluster-autoscaler.yaml"
}
