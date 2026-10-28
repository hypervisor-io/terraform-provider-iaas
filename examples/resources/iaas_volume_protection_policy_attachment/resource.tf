resource "iaas_volume_protection_policy_attachment" "data" {
  policy_id = iaas_volume_protection_policy.nightly.id
  volume_id = iaas_volume.data.id
}
