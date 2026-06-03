# ─────────────────────────────────────────────────────────────────────────────
# Managing the cluster's DEFAULT worker pool with iaas_kubernetes_node_pool
# ─────────────────────────────────────────────────────────────────────────────
#
# A managed Kubernetes cluster always has exactly one DEFAULT worker pool
# (is_default = true). It is created automatically when the cluster is
# provisioned (you can size it at create time via the cluster's worker_count /
# worker_instance_plan_id inputs), so Terraform does NOT *create* it — you ADOPT
# it with `terraform import` and then manage its scale, labels, taints and
# autoscaling in place, exactly like any other node pool.
#
# WHY THIS RATHER THAN A SEPARATE "workers" RESOURCE:
#   The API also exposes cluster-level shim endpoints
#   (POST /workers/scale, PATCH /workers/labels, POST /workers/autoscaling).
#   On the server those are *deprecated backward-compat shims*: each one resolves
#   the cluster's default pool and delegates to the very same per-pool service
#   (WorkersScaleService::scalePoolTo / NodePoolService), then mirrors the result
#   into denormalized cluster.worker_* cache columns. In other words the default
#   pool's scale/labels/autoscaling is ALREADY first-class state on the pool row,
#   and iaas_kubernetes_node_pool is the canonical, non-deprecated way to manage
#   it. A separate singleton "workers" resource would write the same underlying
#   pool through the legacy mirror columns and fight this resource for ownership.

# Adopt the default pool and manage it declaratively. The cluster_id is the
# parent cluster; the pool id is the default pool's UUID (see the import note
# below for how to find it).
resource "iaas_kubernetes_node_pool" "default" {
  cluster_id       = iaas_kubernetes_cluster.prod.id
  name             = "default"
  instance_plan_id = "99999999-9999-9999-9999-999999999999"

  # SCALE the default pool — equivalent to POST /workers/scale. target_count and
  # min_size are kept in lockstep for user edits, so set them to the same value.
  min_size     = 3
  max_size     = 6
  target_count = 3

  # AUTOSCALING the default pool — equivalent to POST /workers/autoscaling. The
  # cluster-autoscaler scales this pool between min_size and max_size.
  autoscaling_enabled = true

  # LABELS / TAINTS on the default pool — equivalent to PATCH /workers/labels.
  labels = {
    "pool" = "default"
  }
  taints = [
    {
      key    = "dedicated"
      value  = "default"
      effect = "PreferNoSchedule"
    },
  ]
}

# The is_default flag confirms this is the cluster default; current_node_count is
# the live worker count (the value the deprecated /workers/scale shim reports).
output "default_pool_is_default" {
  value = iaas_kubernetes_node_pool.default.is_default
}

output "default_pool_nodes" {
  value = iaas_kubernetes_node_pool.default.current_node_count
}

# ── Importing the default pool ───────────────────────────────────────────────
# Node pools have no per-pool SHOW endpoint, so list the cluster's pools and pick
# the one with is_default = true, then import by the COMPOSITE id
# "<cluster_id>/<pool_id>":
#
#   curl -H "Authorization: Bearer $IAAS_API_TOKEN" \
#     "$IAAS_API_ENDPOINT/kubernetes/cluster/<cluster_id>/pools" \
#     | jq -r '.pools[] | select(.is_default) | .id'
#
#   terraform import iaas_kubernetes_node_pool.default <cluster_id>/<default_pool_id>
#
# The default pool cannot be deleted while it is the cluster default (the API
# rejects it with default_pool_protected / default_pool_must_reassign_first), so
# `terraform destroy` of this resource alone is not a supported flow — promote a
# different pool to default first (an operational action), or destroy the whole
# cluster.
