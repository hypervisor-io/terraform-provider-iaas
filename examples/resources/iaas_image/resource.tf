# Capture a custom image (snapshot) from an existing instance. Creation is
# asynchronous: the resource waits for the hypervisor to finish the capture
# (status "available") before returning. There is no update endpoint, so any
# change to name/instance_id/cloudinit/type forces a new resource.
resource "iaas_image" "web_snapshot" {
  instance_id = iaas_instance.web.id
  name        = "web-prod-2024-05"

  # cloudinit = true  # defaults to the source instance's cloud-init setting
  # type      = "linux" # linux | windows | other; defaults to the source instance's type
}
