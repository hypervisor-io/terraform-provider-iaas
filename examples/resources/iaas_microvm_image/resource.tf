resource "iaas_microvm_image" "tools" {
  name         = "tools"
  description  = "Reusable build tools"
  source_kind  = "oci"
  source_image = "ghcr.io/example/tools:1.0"
  location_id  = "00000000-0000-0000-0000-000000000000"

  env = {
    MODE = "production"
  }
}

# A Dockerfile build must name a catalog base image (C6): the builder
# overlays the OCI rootfs on a copy of the base and re-injects init/
# guest-net/envd/vcagent/sshd.
resource "iaas_microvm_image" "app" {
  name          = "app"
  source_kind   = "dockerfile"
  dockerfile    = <<-EOT
    FROM base
    RUN apt-get update && apt-get install -y curl
  EOT
  base_image_id = "22222222-2222-2222-2222-222222222222"
  location_id   = "00000000-0000-0000-0000-000000000000"
}
