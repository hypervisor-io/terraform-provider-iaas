variable "sandbox_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "iaas_microvm_secret" "agent" {
  name          = "ANTHROPIC_API_KEY"
  value         = var.sandbox_secret
  value_version = 1 # Increment when supplying a new value.
}
