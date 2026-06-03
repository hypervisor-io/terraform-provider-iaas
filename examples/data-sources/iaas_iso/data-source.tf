# Look up a mountable ISO by its exact name.
data "iaas_iso" "alma" {
  name = "AlmaLinux 9"
}

output "iso_id" {
  value = data.iaas_iso.alma.id
}
