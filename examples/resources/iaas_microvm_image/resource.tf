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

# A private OCI image pulled with a registry credential stored in the
# sandbox secrets vault. Prefer this over registry_username and
# registry_password, which are deprecated and removed in 3.3.7.
resource "iaas_microvm_image" "private" {
  name               = "private-tools"
  source_kind        = "oci"
  source_image       = "registry.example.com/team/tools:1.0"
  registry_secret_id = "33333333-3333-3333-3333-333333333333"
  location_id        = "00000000-0000-0000-0000-000000000000"
}
