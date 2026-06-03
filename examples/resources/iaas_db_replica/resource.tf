# A read replica of a managed database. The replica is its own database row
# attached to a primary; the engine and version are inherited from the primary.
#
# The primary must be active, a primary (not itself a replica), and in a VPC, and
# the replica plan's storage must be >= the primary plan's. Creation is
# asynchronous (the provider waits for "active"). The primary, name, and subnet
# are immutable; the plan can be resized in place.
resource "iaas_db_replica" "read" {
  primary_id    = iaas_managed_database.primary.id
  db_plan_id    = "00000000-0000-0000-0000-000000000000" # storage >= the primary plan's
  vpc_subnet_id = "22222222-2222-2222-2222-222222222222" # a subnet in the primary's VPC

  # name is optional; the server generates "<primary>-replica-N" when omitted.
  name = "app-prod-db-replica-1"

  timeouts {
    create = "30m"
    delete = "30m"
  }
}

output "replica_host" {
  value = iaas_db_replica.read.host
}

output "replica_replication_status" {
  value = iaas_db_replica.read.replication_status
}
