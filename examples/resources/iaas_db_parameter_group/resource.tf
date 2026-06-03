# A MySQL parameter group using only suffix-free parameters.
#
# The engine is immutable (changing it forces a new resource).
# Parameters are replaced in full on each update.
#
# IMPORTANT — only suffix-free parameters are supported via Terraform.
# Memory-size parameters (innodb_buffer_pool_size, innodb_log_file_size,
# innodb_redo_log_capacity, max_allowed_packet, tmp_table_size,
# max_heap_table_size for MySQL/MariaDB; shared_buffers, effective_cache_size,
# work_mem, maintenance_work_mem, wal_buffers, max_wal_size for PostgreSQL)
# receive a non-idempotent unit suffix from the server on every write and
# cannot be managed here — set them via the control panel instead.
#
# Parameter groups are a billed add-on — billing must be enabled on the account.
#
# Applying a parameter group to a managed database:
#   Use the PATCH /database/{id}/parameter-group API endpoint (not yet modelled
#   as a Terraform attribute; planned for a future iaas_managed_database update).
resource "iaas_db_parameter_group" "mysql_custom" {
  name   = "app-mysql-params"
  engine = "mysql" # mysql | mariadb | postgresql

  # Only suffix-free parameters here. Memory-size params (e.g.
  # innodb_buffer_pool_size) must be set via the control panel.
  parameters = {
    max_connections     = "500"
    wait_timeout        = "3600"
    interactive_timeout = "3600"
    slow_query_log      = "1"
    long_query_time     = "2"
  }
}

# A minimal PostgreSQL parameter group (suffix-free parameters only).
resource "iaas_db_parameter_group" "pg_custom" {
  name   = "app-pg-params"
  engine = "postgresql"

  # Memory-size params (shared_buffers, work_mem, etc.) are not supported here.
  # Set them via the control panel.
  parameters = {
    max_connections                     = "200"
    log_min_duration_statement          = "1000"
    idle_in_transaction_session_timeout = "30000"
  }
}
