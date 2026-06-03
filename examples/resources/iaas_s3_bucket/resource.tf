# An S3-compatible object storage bucket. The name, plan and server are fixed at
# creation (changing any forces a new resource). The bucket gets its own
# auto-generated access_key/secret_key pair (re-readable on every refresh).
resource "iaas_s3_bucket" "assets" {
  name         = "my-app-assets" # 3-63 chars, lowercase, globally unique
  s3_plan_id   = "00000000-0000-0000-0000-000000000000"
  s3_server_id = "11111111-1111-1111-1111-111111111111"

  # Optional default ACL: private (default) | public | upload | download.
  default_access = "private"

  # Optionally grant standalone access keys access to this bucket, each with a
  # permission (read | write | readwrite). The permission is changed in place.
  attached_keys = [
    {
      access_key_id = iaas_s3_access_key.app.id
      permission    = "readwrite"
    },
  ]
}

resource "iaas_s3_access_key" "app" {
  name = "app-key"
}

# The bucket's own credentials (the secret is re-readable, unlike a standalone
# access key whose secret is shown only once).
output "bucket_endpoint" {
  value = iaas_s3_bucket.assets.endpoint
}

output "bucket_access_key" {
  value = iaas_s3_bucket.assets.access_key
}

output "bucket_secret_key" {
  value     = iaas_s3_bucket.assets.secret_key
  sensitive = true
}
