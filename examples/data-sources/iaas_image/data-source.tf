# Look up an OS image by name. Optionally scope the search to a location
# (hypervisor group) to only consider images available there.
data "iaas_image" "ubuntu" {
  name = "Ubuntu 24.04"

  # hypervisor_group_id = data.iaas_location.nyc.id # optional search scope
}

output "image_id" {
  value = data.iaas_image.ubuntu.id
}
