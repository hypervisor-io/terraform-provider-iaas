data "iaas_platform_version" "current" {}

output "min_agent_version" {
  value = data.iaas_platform_version.current.min_agent_version
}
