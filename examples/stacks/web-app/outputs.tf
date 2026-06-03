output "load_balancer_public_ip" {
  description = "Public IP address the load balancer accepts traffic on. Point your DNS at this."
  value       = iaas_load_balancer.web.public_ip
}

output "load_balancer_status" {
  description = "Lifecycle status of the load balancer (deploying → configuring → active)."
  value       = iaas_load_balancer.web.status
}

output "instance_ids" {
  description = "UUIDs of the provisioned web instances."
  value       = [for i in iaas_instance.web : i.id]
}

output "instance_public_ips" {
  description = "Primary public IPs of the web instances."
  value       = [for i in iaas_instance.web : i.primary_public_ip]
}

output "instance_private_ips" {
  description = "Primary private (VPC subnet) IPs of the web instances — these are the load balancer target IPs."
  value       = [for i in iaas_instance.web : i.primary_private_ip]
}

output "vpc_id" {
  description = "UUID of the VPC the stack provisioned."
  value       = iaas_vpc.main.id
}

output "vpc_subnet_id" {
  description = "UUID of the web-tier subnet."
  value       = iaas_vpc_subnet.web.id
}

output "security_group_id" {
  description = "UUID of the web security group attached to the instances."
  value       = iaas_security_group.web.id
}
