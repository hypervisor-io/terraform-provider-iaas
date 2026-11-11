data "iaas_microvm_catalog" "current" {}

output "microvm_locations" {
  value = data.iaas_microvm_catalog.current.locations
}

# Only the plans whose disk holds a kit's image.
data "iaas_microvm_catalog" "for_kit" {
  image_id = data.iaas_microvm_catalog.current.images[0].id
}
