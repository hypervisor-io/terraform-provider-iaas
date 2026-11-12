# A persistent data volume for sandboxes: a disk that keeps its data when a sandbox stops, pauses or is deleted.
# It lives on one node of the location, and the sandbox that mounts it runs on that node.
resource "iaas_sandbox_volume" "data" {
  name        = "agent-data"
  size_gib    = 20
  location_id = var.location_id
}

# Mount it when the sandbox is created. Changing the list later replaces the sandbox (a volume can only be attached
# or detached while the sandbox is stopped); the volume and its data stay.
resource "iaas_microvm" "worker" {
  name        = "worker"
  location_id = var.location_id
  image_id    = var.image_id
  plan_id     = var.plan_id
  network     = [{ kind = "public" }]

  volumes = [{
    volume_id  = iaas_sandbox_volume.data.id
    mount_path = "/data"
  }]
}
