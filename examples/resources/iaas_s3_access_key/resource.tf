# A standalone S3 access key. The secret is returned ONLY once, at creation, and
# is captured into the (sensitive) secret_key attribute and preserved in state.
# A key imported into Terraform will have an empty secret_key (it cannot be
# recovered). The user API exposes no delete endpoint for access keys, so
# destroying this resource only removes it from Terraform state — delete the key
# in the control panel to remove it server-side.
resource "iaas_s3_access_key" "app" {
  name = "app-key"

  # Optional: set false to suspend the key (resume by setting it back to true).
  active = true
}

# The secret is shown once on create and preserved in state thereafter.
output "s3_access_key" {
  value = iaas_s3_access_key.app.access_key
}

output "s3_secret_key" {
  value     = iaas_s3_access_key.app.secret_key
  sensitive = true
}
