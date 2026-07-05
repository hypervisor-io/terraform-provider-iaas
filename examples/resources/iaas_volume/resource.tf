# Provision a block storage volume, then attach it to an instance.
#
# Prerequisites:
#   1. Billing must be enabled on the platform (volumes are metered via Cloud Service).
#   2. The hypervisor group must have volume support enabled (volume_enabled = true)
#      and offer the chosen volume plan.
#   3. volume_plan_id selects the size and IO tier (sizing is PLAN-BASED, not a
#      free-form size_gb). To resize, switch to a larger plan of the same storage
#      class and datastore type - the provider issues an in-place resize.
#
# Creation is asynchronous: the provider waits for the volume's status to become
# "available" before completing. The create timeout is configurable below.
#
# Immutable inputs (changing any forces a new resource): name, hypervisor_group_id,
# project_id. In-place updatable inputs: volume_plan_id (resize) and instance_id
# (attach/detach).

resource "iaas_volume" "data" {
  # Display name. Immutable - changing it replaces the volume.
  name = "app-data"

  # UUID of the volume plan (size + IO limits). Change to a larger plan to resize.
  volume_plan_id = "00000000-0000-0000-0000-000000000010"

  # UUID of the volume-enabled hypervisor group. Immutable.
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"

  # Optional: attach the volume to an instance in place. Clear this to detach.
  # The instance must be in the same hypervisor group as the volume.
  instance_id = "00000000-0000-0000-0000-000000000020"

  # Optional: organise the volume under a project. Immutable.
  # project_id = "00000000-0000-0000-0000-000000000030"

  timeouts {
    create = "30m"
  }
}

# The provisioned size in GB (derived from the plan; changes on resize).
output "volume_size_gb" {
  value = iaas_volume.data.size
}

# The guest device name once attached (e.g. "xvda"), empty when detached.
output "volume_device" {
  value = iaas_volume.data.dev
}
