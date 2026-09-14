resource "iaas_microvm_image" "tools" {
  name                = "tools"
  description         = "Reusable build tools"
  source_kind         = "oci"
  source_image        = "ghcr.io/example/tools:1.0"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"

  env = {
    MODE = "production"
  }
}
