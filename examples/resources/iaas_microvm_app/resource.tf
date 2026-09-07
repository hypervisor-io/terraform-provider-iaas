# Deploy a Serverless App: a scale-to-zero microVM app built from a container
# image (source_kind = "oci") or a git repository (source_kind = "git"),
# reachable at an instant <slug>.apps.<domain> URL.
#
# hypervisor_group_id, slug and source_kind are immutable: changing any of
# them destroys and recreates the app (no rename/re-home endpoint exists).
# Changing source_image/source_repo/source_branch - or any other tracked
# field - re-runs the create-and-deploy flow: the API builds a fresh revision
# and deploys it.
resource "iaas_microvm_app" "demo" {
  # Location (hypervisor group) UUID the app is placed in.
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
  slug                = "demo"
  source_kind         = "oci"
  source_image        = "ghcr.io/acme/demo:latest"

  # Optional; when omitted the server defaults apply (port 8080, scale to
  # zero, 300s idle timeout, "/" health path).
  port                 = 8080
  min_instances        = 0
  idle_timeout_seconds = 300
}

# A git-sourced app builds from a repository branch instead of an image:
#
#   resource "iaas_microvm_app" "from_git" {
#     hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
#     slug                = "from-git"
#     source_kind         = "git"
#     source_repo         = "https://github.com/acme/demo.git"
#     source_branch       = "main"
#   }

# The app's URL and lifecycle state are exported read-only:
output "microvm_app_fqdn" {
  value = iaas_microvm_app.demo.fqdn
}

output "microvm_app_state" {
  value = iaas_microvm_app.demo.state
}
