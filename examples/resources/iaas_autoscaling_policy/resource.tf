# Scale the web fleet up when sustained CPU exceeds 80%, down below 30%.
resource "iaas_autoscaling_policy" "web_cpu" {
  group_id = iaas_autoscaling_group.web.id

  metric               = "cpu"
  scale_up_threshold   = 80
  scale_down_threshold = 30

  # Optional tuning (server defaults apply when omitted).
  scale_up_step       = 2
  scale_down_step     = 1
  scale_up_cooldown   = 300
  scale_down_cooldown = 600
  evaluation_interval = 30
  evaluation_window   = 120
}

# A second policy on the same group driven by memory pressure.
resource "iaas_autoscaling_policy" "web_mem" {
  group_id = iaas_autoscaling_group.web.id

  metric               = "memory"
  scale_up_threshold   = 85
  scale_down_threshold = 40
}

# Import an existing policy with the composite "<group_id>/<policy_id>" id:
#   terraform import iaas_autoscaling_policy.web_cpu <group_id>/<policy_id>
