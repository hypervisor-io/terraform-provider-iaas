# A MySQL parameter group with custom configuration.
#
# The engine is immutable (changing it forces a new resource).
# Parameters are replaced in full on each update.
#
# Parameter groups are a billed add-on — billing must be enabled on the account.
#
# Applying a parameter group to a managed database:
#   Use the PATCH /database/{id}/parameter-group API endpoint (not yet modelled
#   as a Terraform attribute; planned for a future iaas_managed_database update).
resource "iaas_db_parameter_group" "mysql_custom" {
  name   = "app-mysql-params"
  engine = "mysql" # mysql | mariadb | postgresql

  # Key→value map of database engine parameters.
  # Provide values as strings in the form the API returns them.
  # The server validates keys against its per-engine catalog.
  parameters = {
    max_connections         = "500"
    innodb_buffer_pool_size = "512M"
    slow_query_log          = "1"
  }
}

# A minimal PostgreSQL parameter group.
resource "iaas_db_parameter_group" "pg_custom" {
  name   = "app-pg-params"
  engine = "postgresql"

  parameters = {
    max_connections = "200"
  }
}
