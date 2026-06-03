# An additional worker node pool on a managed Kubernetes cluster. Each pool is a
# CHILD of a cluster (its cluster_id is part of the API path, so changing it
# forces a new resource) and is backed by its own instance plan, sizing bounds,
# labels, taints and autoscaling toggle.
#
# Creation is SYNCHRONOUS (the pool row is recorded immediately); worker VMs are
# then provisioned asynchronously in the background, so the live worker count
# (current_node_count) populates on subsequent reads.
#
# NOTE: for user-driven edits the API keeps min_size and target_count in lockstep
# — set them to the same value (supplying only one mirrors it to the other).

resource "iaas_kubernetes_node_pool" "gpu" {
  cluster_id       = iaas_kubernetes_cluster.prod.id
  name             = "gpu-pool" # DNS-1123 style, unique within the cluster
  instance_plan_id = "99999999-9999-9999-9999-999999999999"

  # Sizing — min_size and target_count are kept equal for user edits.
  min_size     = 2
  max_size     = 5
  target_count = 2

  # Cluster-autoscaler may grow/shrink this pool between min_size and max_size.
  autoscaling_enabled = true
  weight              = 50 # scaler preference when several pools are eligible (1-100)

  # Kubernetes node labels applied to every node in this pool.
  labels = {
    "workload" = "gpu"
  }

  # Kubernetes node taints applied to every node in this pool.
  taints = [
    {
      key    = "nvidia.com/gpu"
      value  = "present"
      effect = "NoSchedule"
    },
  ]
}

# All of name, instance_plan_id, min_size/max_size/target_count, weight,
# autoscaling_enabled, labels and taints are updatable in place. Changing
# instance_plan_id is only accepted while the pool has no live workers (scale to
# zero first). Scaling target_count provisions or drains worker VMs.

# The default-pool flag and the live worker count are exported (server-managed):
output "gpu_pool_is_default" {
  value = iaas_kubernetes_node_pool.gpu.is_default
}

output "gpu_pool_nodes" {
  value = iaas_kubernetes_node_pool.gpu.current_node_count
}

# Import an existing node pool by its COMPOSITE id "<cluster_id>/<pool_id>", e.g.:
#   terraform import iaas_kubernetes_node_pool.gpu 11111111-1111-1111-1111-111111111111/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
