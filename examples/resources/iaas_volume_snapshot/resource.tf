# Take a point-in-time snapshot of a volume.
#
# A snapshot is a CHILD of a volume: the parent volume_id is part of the API path,
# so changing it forces a new resource. Creation is asynchronous — the provider
# enqueues the snapshot and waits for it to become "available".
#
# Snapshots are immutable: there is no update endpoint, so changing the name
# forces a new snapshot. Use a UNIQUE name per volume — the provider resolves the
# server-assigned snapshot id by matching the name in the parent volume's snapshot
# list (the create endpoint returns a job queue, not the snapshot id).
#
# Import uses a composite id:  terraform import iaas_volume_snapshot.nightly <volume_id>/<snapshot_id>

resource "iaas_volume" "data" {
  name                = "app-data"
  volume_plan_id      = "00000000-0000-0000-0000-000000000010"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
}

resource "iaas_volume_snapshot" "nightly" {
  # Parent volume. Changing this forces a new snapshot.
  volume_id = iaas_volume.data.id

  # Unique snapshot name (also the id-resolution key). Immutable.
  name = "nightly-2026-06-03"

  timeouts {
    create = "30m"
    delete = "30m"
  }
}

# Captured size in bytes, populated once the snapshot completes.
output "snapshot_size_bytes" {
  value = iaas_volume_snapshot.nightly.size
}
