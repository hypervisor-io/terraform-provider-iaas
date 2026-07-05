# Deploys a Docker app onto an instance. Creation is asynchronous and
# TWO-PHASE:
#
#   1. If the instance does not yet have the Docker engine installed
#      (instance.docker_enabled == 0), Create calls the install endpoint and
#      polls until it converges - no separate resource/step required.
#   2. The app or compose file is deployed and this resource waits for the
#      deployment to report "running" (fails on "error"/"failed").
#
# There is no update route beyond start/stop/restart/remove control actions
# (not modelled in this version), so changing ANY attribute forces a new
# resource (destroy + redeploy).

resource "iaas_instance" "app_host" {
  location_id = "11111111-1111-1111-1111-111111111111"
  plan_id     = "22222222-2222-2222-2222-222222222222"
  image_id    = "33333333-3333-3333-3333-333333333333" # must be a Linux image
}

# ── source = "app": a catalog app ──────────────────────────────────────────
resource "iaas_docker_deployment" "wordpress" {
  instance_id = iaas_instance.app_host.id

  source = "app"
  slug   = "wordpress" # see the docker catalog (GET .../docker/apps) for slugs

  env = {
    WORDPRESS_DB_PASSWORD = "change-me"
  }

  port_mappings = [
    {
      container_port = 80
      host_port      = 8080
      # protocol = "tcp" # tcp | udp; defaults to tcp
    }
  ]
}

# ── source = "compose": a custom docker-compose.yml fetched from a URL ─────
resource "iaas_docker_deployment" "custom_app" {
  instance_id = iaas_instance.app_host.id

  source      = "compose"
  compose_url = "https://raw.githubusercontent.com/example/repo/main/docker-compose.yml"
  name        = "my-custom-app" # sent as app_name; required for source = "compose"

  env = {
    APP_ENV = "production"
  }
}

output "wordpress_status" {
  value = iaas_docker_deployment.wordpress.status
}

# Import an existing deployment with the COMPOSITE id "<instance_id>/<deployment_id>".
# NOTE: env/port_mappings cannot be read back (write-only) and land null/empty
# on import; re-set them in configuration if they matter (they have no update
# path, so a subsequent apply without them is otherwise harmless). e.g.:
#   terraform import iaas_docker_deployment.wordpress 11111111-1111-1111-1111-111111111111/44444444-4444-4444-4444-444444444444
