# A managed MySQL database deployed into a VPC subnet.
#
# Creation is asynchronous: the provider waits for the database to become
# "active". The engine, name, and network placement are immutable (changing any
# forces a new resource); the plan can be resized in place, and engine_version
# can be raised in place (an in-place major-version upgrade — see the
# engine_version attribute description for the async-timing caveat).
#
# Managed databases are a billed add-on — billing must be enabled on the account.
resource "iaas_managed_database" "primary" {
  name           = "app-prod-db"
  engine         = "mysql" # mysql | mariadb | postgresql
  engine_version = "8.0"   # raise this in place to upgrade (target must be higher)
  db_plan_id     = "00000000-0000-0000-0000-000000000000" # an enabled db_plan supporting the engine
  vpc_id         = "11111111-1111-1111-1111-111111111111"
  vpc_subnet_id  = "22222222-2222-2222-2222-222222222222" # public subnet → public host; private subnet needs a NAT gateway

  # Optional: changing this token rotates the admin password and exposes the new
  # cleartext value in the (sensitive) password attribute. The API never returns
  # the password on create/read, so leave reset_password unset if you do not need
  # the provider to manage it.
  reset_password = "rotate-2026-06"

  # Optional: changing this token resyncs every eligible replica of this primary
  # (only meaningful once one or more iaas_db_replica children exist). Leave unset
  # if you do not need the provider to trigger a resync.
  # resync_replicas = "resync-2026-06"

  timeouts {
    create = "30m"
    update = "15m" # sizes the engine_version upgrade wait
    delete = "30m"
  }
}

# Connection details (host is empty for private-subnet databases).
output "db_host" {
  value = iaas_managed_database.primary.host
}

output "db_port" {
  value = iaas_managed_database.primary.port
}

output "db_username" {
  value = iaas_managed_database.primary.username
}

output "db_password" {
  value     = iaas_managed_database.primary.password
  sensitive = true
}

# Alert state surfaced from the API (T9): last_error is non-empty and
# error_acknowledged is false while an unacknowledged failure is outstanding.
output "db_last_error" {
  value = iaas_managed_database.primary.last_error
}
