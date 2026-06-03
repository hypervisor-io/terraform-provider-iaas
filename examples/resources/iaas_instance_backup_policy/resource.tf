# Daily full backup with up to 3 incrementals, 7-day retention, primary disk
# only. Attach two instances to this policy.
resource "iaas_instance_backup_policy" "daily" {
  name                  = "daily-primary-disk"
  full_backup_frequency = "daily"
  full_backup_time      = "02:00"
  max_incremental_chain = 3
  retention_count       = 7
  backup_device         = "primary"

  instance_ids = [
    iaas_instance.web.id,
    iaas_instance.app.id,
  ]
}

# Weekly full backup on Sunday at 03:00 UTC, all disks, 30-day retention.
resource "iaas_instance_backup_policy" "weekly" {
  name                  = "weekly-all-disks"
  full_backup_frequency = "weekly"
  full_backup_time      = "03:00"
  full_backup_day       = 0 # 0 = Sunday
  max_incremental_chain = 6
  retention_count       = 30
  backup_device         = "all"
}
