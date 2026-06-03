# A managed MySQL database deployed into a VPC subnet.
#
# Creation is asynchronous: the provider waits for the database to become
# "active". The engine, version, name, and network placement are immutable
# (changing any forces a new resource); the plan can be resized in place.
#
# Managed databases are a billed add-on — billing must be enabled on the account.
resource "iaas_managed_database" "primary" {
  name           = "app-prod-db"
  engine         = "mysql" # mysql | mariadb | postgresql
  engine_version = "8.0"
  db_plan_id     = "00000000-0000-0000-0000-000000000000" # an enabled db_plan supporting the engine
  vpc_id         = "11111111-1111-1111-1111-111111111111"
  vpc_subnet_id  = "22222222-2222-2222-2222-222222222222" # public subnet → public host; private subnet needs a NAT gateway

  # Optional: changing this token rotates the admin password and exposes the new
  # cleartext value in the (sensitive) password attribute. The API never returns
  # the password on create/read, so leave reset_password unset if you do not need
  # the provider to manage it.
  reset_password = "rotate-2026-06"

  timeouts {
    create = "30m"
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
