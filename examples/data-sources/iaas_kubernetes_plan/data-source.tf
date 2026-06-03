# Resolve Kubernetes cluster plan UUIDs by name. The `kind` argument selects the
# picker: `worker`/`cp` resolve instance plans (the same underlying list), and
# `lb` resolves the control-plane load-balancer plan.

data "iaas_kubernetes_plan" "worker" {
  kind = "worker"
  name = "std-2"
}

data "iaas_kubernetes_plan" "control_plane" {
  kind = "cp"
  name = "cp-2"
}

data "iaas_kubernetes_plan" "cp_lb" {
  kind = "lb"
  name = "lb-2"
}

# Feed the resolved ids straight into an iaas_kubernetes_cluster:
#
# resource "iaas_kubernetes_cluster" "prod" {
#   worker_instance_plan_id = data.iaas_kubernetes_plan.worker.id
#   cp_instance_plan_id     = data.iaas_kubernetes_plan.control_plane.id
#   cp_lb_plan_id           = data.iaas_kubernetes_plan.cp_lb.id
#   # ...
# }

output "worker_plan_id" {
  value = data.iaas_kubernetes_plan.worker.id
}
