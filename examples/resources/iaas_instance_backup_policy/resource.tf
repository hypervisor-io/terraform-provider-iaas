# Daily full backup with up to 3 incrementals, 7-chain retention, primary disk
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

# Weekly full backup on Sunday at 03:00 in the account owner timezone, all disks, 30-chain retention.
resource "iaas_instance_backup_policy" "weekly" {
  name                  = "weekly-all-disks"
  full_backup_frequency = "weekly"
  full_backup_time      = "03:00"
  full_backup_day       = 0 # 0 = Sunday
  max_incremental_chain = 6
  retention_count       = 30
  backup_device         = "all"
}

# GFS retention follows an explicit named IANA timezone, including DST.
# A rule keeps the newest backup in each selected calendar bucket; keep_last
# is a floor. Zero/null disables a rule. At least one rule must be positive.
resource "iaas_instance_backup_policy" "gfs" {
  name                  = "weekly-gfs"
  full_backup_frequency = "weekly"
  full_backup_time      = "02:30"
  full_backup_day       = 0
  timezone              = "Europe/Amsterdam"
  max_incremental_chain = 6
  backup_device         = "all"
  retention_mode        = "gfs"
  keep_last             = 2
  keep_within_days      = 30
  keep_daily            = 7
  keep_weekly           = 4
  keep_monthly          = 6
  keep_yearly           = 1
}
# Remove timezone (or set null) to return to the account owner timezone.
# A zone change resubmits the configured time/day, preserving this local schedule.
