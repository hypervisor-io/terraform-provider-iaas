resource "iaas_ssh_key" "example" {
  name = "laptop"

  # The SSH public key material. Changing this forces a new resource.
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILcl0K3kLJv6N4F2bWj2vJxQ1q3qY8m0Q3a2bC4d5E6F you@example.com"
}
