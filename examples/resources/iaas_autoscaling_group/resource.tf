# A self-healing web fleet kept between 2 and 8 instances.
resource "iaas_autoscaling_group" "web" {
  name                = "web-asg"
  hypervisor_group_id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  plan_id             = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
  image_id            = "cccccccc-cccc-cccc-cccc-cccccccccccc"

  min_instances = 2
  max_instances = 8

  # SSH keys are injected at launch and are fixed for the life of the group
  # (changing them forces a new group).
  ssh_keys = ["dddddddd-dddd-dddd-dddd-dddddddddddd"]

  # Security groups attached to every new instance (updatable in place).
  security_group_ids = ["eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"]

  cloud_init = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - nginx
  EOT
}

# A group placed inside a VPC subnet and auto-registered with a load balancer
# backend. The placement (VPC/subnet/LB) is fixed at create time.
resource "iaas_autoscaling_group" "api" {
  name                = "api-asg"
  hypervisor_group_id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  plan_id             = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
  image_id            = "cccccccc-cccc-cccc-cccc-cccccccccccc"

  vpc_id        = iaas_vpc.main.id
  vpc_subnet_id = iaas_vpc_subnet.app.id

  load_balancer_id = iaas_load_balancer.edge.id
  lb_backend_id    = iaas_lb_backend.api.id

  min_instances = 1
  max_instances = 5

  # Start the group paused so it doesn't scale until policies are attached.
  paused = true
}
