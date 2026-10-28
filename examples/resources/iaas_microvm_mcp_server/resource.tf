variable "mcp_bearer_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "iaas_microvm_mcp_server" "tools" {
  provider_slug        = "custom"
  name                 = "tools"
  label                = "Team tools"
  url                  = "https://tools.example.com/mcp"
  auth_kind            = "bearer"
  enabled              = true
  static_token         = var.mcp_bearer_token
  static_token_version = 1 # Increment to rotate without changing the revision.
  headers              = {}
}
