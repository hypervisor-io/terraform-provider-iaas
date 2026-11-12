# Find the free volumes of the account and see what each other volume is attached to.
data "iaas_sandbox_volumes" "free" {
  status = "available"
}

output "free_volume_ids" {
  value = [for v in data.iaas_sandbox_volumes.free.volumes : v.id]
}
