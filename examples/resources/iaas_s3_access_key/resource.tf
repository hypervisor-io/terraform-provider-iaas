# A standalone S3 access key. The secret is returned ONLY once, at creation, and
# is captured into the (sensitive) secret_key attribute and preserved in state.
# A key imported into Terraform will have an empty secret_key (it cannot be
# recovered). Destroying the resource deletes the key and removes its credentials
# from the storage servers.
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
