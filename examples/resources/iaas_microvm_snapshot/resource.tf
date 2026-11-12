# Save the disk of a sandbox. Create waits until the snapshot is ready. On locations without a fast-copy file
# system a running sandbox may pause for a few seconds (see paused_ms); pause or stop it first to avoid that.
resource "iaas_microvm_snapshot" "golden" {
  sandbox_id  = iaas_microvm.base.id
  name        = "golden-image"
  description = "Installed and configured, before the first customer job"
}

output "snapshot_disk_gib" {
  value = iaas_microvm_snapshot.golden.disk_gib
}
