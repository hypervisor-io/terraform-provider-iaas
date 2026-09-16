data "iaas_microvm_images" "ready" {
  status = "ready"
}

output "microvm_image_ids" {
  value = [for image in data.iaas_microvm_images.ready.images : image.id]
}
