# Whoami: the account behind the provider's configured token. This is a
# SINGLETON data source with no input filter - referencing it validates the
# token + IP-lock during plan/apply, so a misconfigured provider fails fast.

data "iaas_account" "current" {}

output "account_id" {
  value = data.iaas_account.current.id
}

output "is_admin" {
  value = data.iaas_account.current.is_admin
}
