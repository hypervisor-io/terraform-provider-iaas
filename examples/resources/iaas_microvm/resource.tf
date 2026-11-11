resource "iaas_microvm" "worker" {
  name        = "worker"
  location_id = "00000000-0000-0000-0000-000000000000"
  image_id    = "00000000-0000-0000-0000-000000000000"
  plan_id     = "00000000-0000-0000-0000-000000000000"

  # Account SSH keys installed to /root/.ssh/authorized_keys and every
  # non-system user's authorized_keys.
  ssh_key_ids = ["11111111-1111-1111-1111-111111111111"]

  ingress = {
    http = {
      enabled     = true
      port        = 8080
      health_path = "/health"
    }
  }

  # There is no isolated kind - every microVM carries at least one public or
  # vpc interface. A public entry with no subnet_id lets the platform
  # auto-assign a subnet with free capacity in this location.
  network = [{
    kind = "public"
  }]

  env = {
    MODE = "production"
  }

  # Outbound network policy: deny everything except the listed destinations.
  # It is in force from the first boot and changes in place afterwards;
  # removing the attribute removes the policy (unrestricted outbound access).
  egress_policy = {
    default_action = "deny"
    rules = [
      {
        action = "allow"
        target = "api.github.com"
        ports  = ["443"]
      },
      {
        action   = "allow"
        target   = "*.pythonhosted.org"
        protocol = "tcp"
        ports    = ["443"]
        comment  = "package downloads"
      },
      {
        action   = "allow"
        target   = "8.8.8.8"
        protocol = "tcp"
        ports    = ["443", "8000-8100"]
      },
    ]
  }
}
