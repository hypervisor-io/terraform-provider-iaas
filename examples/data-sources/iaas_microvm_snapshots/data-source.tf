# Find a snapshot by name and start sandboxes from it.
data "iaas_microvm_snapshots" "golden" {
  name   = "golden-image"
  status = "ready"
}

resource "iaas_microvm" "worker" {
  count       = 3
  name        = "worker-${count.index}"
  snapshot_id = data.iaas_microvm_snapshots.golden.snapshots[0].id
  # The location is the snapshot's own. The plan must hold the snapshot's root filesystem: any plan when it is 2 GiB or less, otherwise a plan disk of at least its size in whole GiB (disk_gib is the source's plan, not the test).
  plan_id = var.plan_id
  network = [{ kind = "public" }]
  env     = { ROLE = "worker" }
}
