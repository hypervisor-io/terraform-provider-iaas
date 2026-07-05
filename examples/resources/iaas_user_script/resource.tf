resource "iaas_user_script" "bootstrap" {
  name        = "bootstrap"
  type        = "bash"
  description = "Update packages on first boot"
  shebang     = "#!/bin/bash"
  content     = <<-EOT
    apt-get update
    apt-get upgrade -y
  EOT
}
