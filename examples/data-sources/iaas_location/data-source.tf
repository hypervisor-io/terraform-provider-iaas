# Look up a deploy location (hypervisor group) by slug or display name.
data "iaas_location" "nyc" {
  name = "nyc"
}

output "location_id" {
  value = data.iaas_location.nyc.id
}
