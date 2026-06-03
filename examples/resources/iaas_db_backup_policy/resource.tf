# Off-host S3 backup policy for managed databases: daily full + 6h incrementals,
# 7-day full retention, 14-day incremental retention, 72h PITR history.
resource "iaas_db_backup_policy" "prod" {
  name      = "prod-databases"
  s3_endpoint = "s3.us-east-1.amazonaws.com"
  s3_bucket   = "my-company-db-backups"
  s3_region   = "us-east-1"

  # Credentials are stored encrypted server-side and never returned by the API.
  # They are preserved in Terraform state between refreshes.
  s3_access_key = var.db_backup_s3_access_key
  s3_secret_key = var.db_backup_s3_secret_key

  s3_path_prefix = "prod/mysql"

  full_backup_frequency = "daily"
  full_backup_time      = "01:00"
  incremental_frequency = "6h"

  pitr_enabled = true

  retention_full_count       = 7
  retention_incremental_days = 14
  retention_pitr_hours       = 72

  encryption_enabled = true

  # Attach managed databases to this policy.
  database_ids = [
    iaas_managed_database.app.id,
    iaas_managed_database.analytics.id,
  ]
}

variable "db_backup_s3_access_key" {
  type      = string
  sensitive = true
}

variable "db_backup_s3_secret_key" {
  type      = string
  sensitive = true
}
