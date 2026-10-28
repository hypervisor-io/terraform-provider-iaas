resource "iaas_volume_protection_policy" "nightly" {
  name             = "Nightly volume protection"
  frequency        = "daily"
  schedule_time    = "02:30"
  timezone         = "Europe/Amsterdam"
  backup_enabled   = true
  snapshot_enabled = true
  retention_mode   = "gfs"
  keep_last        = 2
  keep_daily       = 7
  keep_weekly      = 4
}
