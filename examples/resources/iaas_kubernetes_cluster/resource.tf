# A managed Kubernetes cluster: a control plane (1 or 3 nodes) behind a dedicated
# CP load balancer, plus a default worker node pool. Creation is ASYNCHRONOUS and
# multi-stage — this resource waits for the cluster state to become "running"
# (created -> starting -> running, or "error" on failure).
#
# PRE-REQS (the API enforces these, returning a clear error otherwise):
#   - the region (hypervisor_group) must have Kubernetes + VPC + Load Balancer enabled,
#   - the VPC must have an active NAT gateway with a public IP,
#   - cp_vpc_subnet_id must be a PRIVATE subnet,
#   - control_node_count must be 1 or 3 (3 is required when lb_ha_enabled = true).
#
# This is the CORE cluster only. Node pools, worker scaling, version upgrades,
# kubeconfig download, and per-cluster security/SSL config are managed by SEPARATE
# resources / data sources.

resource "iaas_kubernetes_cluster" "prod" {
  name = "prod"
  slug = "prod" # url-safe, unique within the account, immutable

  # Region + networking (all immutable — changing any forces a new cluster).
  hypervisor_group_id  = "22222222-2222-2222-2222-222222222222"
  vpc_id               = "33333333-3333-3333-3333-333333333333"
  cp_vpc_subnet_id     = "44444444-4444-4444-4444-444444444444" # MUST be private
  worker_vpc_subnet_id = "55555555-5555-5555-5555-555555555555"

  # Kubernetes version + control-plane topology.
  kubernetes_version_id = "66666666-6666-6666-6666-666666666666"
  control_node_count    = 3                    # 1 (single CP) or 3 (HA CP)
  endpoint_mode         = "public_and_private" # or "private"

  # Sizing plans.
  cp_instance_plan_id     = "77777777-7777-7777-7777-777777777777"
  cp_lb_plan_id           = "88888888-8888-8888-8888-888888888888"
  worker_instance_plan_id = "99999999-9999-9999-9999-999999999999"

  # Default worker pool: initial size (ongoing scaling is a separate operation).
  worker_count = 3

  # Optional create-time tunables (immutable; sensible server defaults apply):
  # description                    = "production cluster"
  # project_id                     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  # pod_cidr                       = "10.244.0.0/16"
  # service_cidr                   = "10.96.0.0/12"
  # lb_ha_enabled                  = true # requires control_node_count = 3 + an HA region
  # pod_security_admission_default = "baseline" # privileged | baseline | restricted

  # K8s provisioning is slow; tune the async waits if needed.
  timeouts {
    create = "45m"
    delete = "30m"
  }
}

# Only name, description, and project_id are mutable in place. Changing slug, the
# version, the plans, the CIDRs, the subnets, control_node_count, endpoint_mode,
# or worker_count forces a new cluster (use the dedicated upgrade / worker-scale
# workflows for in-place topology changes).

# The lifecycle state and the (non-secret) API server endpoints are exported:
output "k8s_state" {
  value = iaas_kubernetes_cluster.prod.state # "running" once ready
}

output "k8s_version" {
  value = iaas_kubernetes_cluster.prod.kubernetes_version # e.g. "1.30.2"
}

output "k8s_api_endpoint" {
  value = iaas_kubernetes_cluster.prod.endpoint_url_public
}

# The kubeconfig and cluster CA are SENSITIVE and are fetched via a separate
# data source (not this resource).

# Import an existing cluster by its UUID, e.g.:
#   terraform import iaas_kubernetes_cluster.prod 11111111-1111-1111-1111-111111111111
