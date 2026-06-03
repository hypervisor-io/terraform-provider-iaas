# Notification channel to send alerts through
resource "iaas_notification_channel" "slack_ops" {
  name = "ops-slack"
  type = "slack"
  config = {
    webhook_url = "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX"
  }
}

# Alert rule watching CPU on a specific instance
resource "iaas_alert_rule" "high_cpu" {
  name          = "High CPU on web-01"
  resource_type = "instance"
  resource_id   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  metric        = "cpu_pct"
  operator      = "gt"
  threshold     = 80
  duration      = 300
  channel_ids   = [iaas_notification_channel.slack_ops.id]
}

# Alert rule watching memory on ALL instances (no resource_id)
resource "iaas_alert_rule" "global_mem" {
  name          = "High Memory (all instances)"
  resource_type = "instance"
  metric        = "mem_pct"
  operator      = "gt"
  threshold     = 90
  duration      = 60
  # re-alert every hour while still breached
  reminder_interval = 3600
  channel_ids       = [iaas_notification_channel.slack_ops.id]
}

# Alert rule targeting a managed database, currently disabled
resource "iaas_alert_rule" "db_disk" {
  name          = "DB disk warning"
  resource_type = "managed_database"
  resource_id   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
  metric        = "disk_pct"
  operator      = "gte"
  threshold     = 75
  enabled       = false
  channel_ids   = [iaas_notification_channel.slack_ops.id]
}
