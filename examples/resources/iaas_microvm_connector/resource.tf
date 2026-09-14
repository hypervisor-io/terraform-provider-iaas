resource "iaas_microvm_connector" "github" {
  kind                = "github_app"
  name                = "github-actions"
  git_source_id       = "00000000-0000-0000-0000-000000000000"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
  plan_id             = "00000000-0000-0000-0000-000000000000"
  image_id            = "00000000-0000-0000-0000-000000000000"
  labels              = ["self-hosted", "linux", "x64"]
  max_concurrent      = 4
  enabled             = true
}
