# Slack notification channel
resource "iaas_notification_channel" "slack_ops" {
  name = "ops-slack"
  type = "slack"
  config = {
    webhook_url = "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX"
  }
}

# Discord notification channel
resource "iaas_notification_channel" "discord_alerts" {
  name = "alerts-discord"
  type = "discord"
  config = {
    webhook_url = "https://discord.com/api/webhooks/000000000000000000/XXXXXXXXXXXX"
  }
}

# Telegram notification channel
resource "iaas_notification_channel" "telegram_bot" {
  name = "telegram-alerts"
  type = "telegram"
  config = {
    bot_token = "123456789:ABCDefGhIJKlmNoPQRsTUVwxyz"
    chat_id   = "-1001234567890"
  }
}

# Generic webhook notification channel with optional settings
resource "iaas_notification_channel" "webhook_custom" {
  name    = "custom-webhook"
  type    = "webhook"
  enabled = true
  config = {
    url            = "https://example.com/hooks/alerts"
    method         = "POST"
    secret         = "my-hmac-signing-secret"
    connect_timeout = "10"
    timeout        = "30"
    verify_ssl     = "1"
  }
}
