data "iaas_webhook_event_kinds" "all" {}

output "webhook_event_kinds" {
  value = data.iaas_webhook_event_kinds.all.kinds
}
