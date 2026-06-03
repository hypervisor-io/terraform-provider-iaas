variable "iaas_api_endpoint" {
  type        = string
  description = "Base URL of the platform user API (e.g. https://panel.example.com). May also be set via the IAAS_API_ENDPOINT environment variable, in which case this can be left empty."
  default     = ""
}

variable "iaas_api_token" {
  type        = string
  description = "IP-locked Bearer token for the platform user API. PREFER setting this via the IAAS_API_TOKEN environment variable instead of a tfvars file so the secret never lands on disk."
  default     = ""
  sensitive   = true
}

variable "location_name" {
  type        = string
  description = "Slug or display name of the deploy location (hypervisor group) the stack is provisioned in, e.g. \"nyc\"."
}

variable "plan_name" {
  type        = string
  description = "Name of the instance plan to size the web instances with, looked up within the location, e.g. \"s1.small\"."
}

variable "plan_group" {
  type        = string
  description = "Optional plan-group disambiguator, only needed when the same plan name exists in more than one group within the location."
  default     = null
}

variable "image_name" {
  type        = string
  description = "Name of the OS image to deploy on the web instances, e.g. \"Ubuntu 24.04\"."
}

variable "hypervisor_group_id" {
  type        = string
  description = "UUID of the VPC-enabled / load-balancer-enabled hypervisor group (location). The VPC and load balancer require this raw id, whereas the instances resolve their location by name. Typically the same physical location as location_name."
}

variable "lb_plan_id" {
  type        = string
  description = "UUID of an enabled load balancer plan. This sizes the backing instance the load balancer runs on."
}

variable "ssh_public_key" {
  type        = string
  description = "SSH public key material (e.g. an ssh-ed25519 line) injected into the web instances at deploy time."
}

variable "web_instance_count" {
  type        = number
  description = "Number of identical web instances to provision behind the load balancer."
  default     = 2
}

variable "vpc_cidr" {
  type        = string
  description = "RFC1918 CIDR block for the VPC private network."
  default     = "10.0.0.0/24"
}

variable "subnet_cidr" {
  type        = string
  description = "IPv4 CIDR for the web-tier subnet inside the VPC. Must fall within vpc_cidr."
  default     = "10.0.0.0/24"
}
