data "iaas_microvm_catalog" "current" {}

output "microvm_locations" {
  value = data.iaas_microvm_catalog.current.locations
}
