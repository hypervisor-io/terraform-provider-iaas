resource "iaas_microvm" "worker" {
  name                = "worker"
  hypervisor_group_id = "00000000-0000-0000-0000-000000000000"
  image_id            = "00000000-0000-0000-0000-000000000000"
  plan_id             = "00000000-0000-0000-0000-000000000000"

  ingress = {
    http = {
      enabled     = true
      port        = 8080
      health_path = "/health"
    }
  }

  network = [{
    kind = "isolated"
  }]

  env = {
    MODE = "production"
  }
}
