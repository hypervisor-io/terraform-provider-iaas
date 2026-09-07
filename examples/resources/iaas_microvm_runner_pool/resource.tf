# GitLab runner pools are fully managed here: create, update, delete. The
# gitlab_token is WRITE-ONLY: it is verified against GitLab on create and
# never returned by the API afterwards, so it is preserved in state (and can
# never be recovered for a pool created outside Terraform).
resource "iaas_microvm_runner_pool" "gitlab" {
  provider_type       = "gitlab"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
  plan_id             = "00000000-0000-0000-0000-000000000001"

  gitlab_url          = "https://gitlab.example.com"
  gitlab_token        = "glrt-xxxxxxxxxxxxxxxxxxxx"
  gitlab_tag_list     = ["microvm"]
  gitlab_run_untagged = false
  warm_count          = 2
}

# GitHub pools CANNOT be created here: install the GitHub App from the
# panel's CI Runners page first, then adopt the linked pool with
#
#   terraform import iaas_microvm_runner_pool.github <pool-id>
#
# and manage it from then on (labels, max_concurrent, enabled, plan_id,
# hypervisor_group_id):
#
#   resource "iaas_microvm_runner_pool" "github" {
#     provider_type       = "github"
#     hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
#     plan_id             = "00000000-0000-0000-0000-000000000002"
#     labels              = ["self-hosted", "linux", "x64"]
#     max_concurrent      = 4
#     enabled             = true
#   }
