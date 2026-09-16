# Per-account default network for microVM/E2B creates that omit `network`.
# There is exactly one of these per account - creating it sets the default;
# destroying it clears the default (PUT default_network = null).

resource "iaas_microvm_settings" "default" {
  default_network = {
    kind = "public"
  }
}

# A VPC default names the owned VPC subnet.
# resource "iaas_microvm_settings" "default" {
#   default_network = {
#     kind          = "vpc"
#     vpc_subnet_id = "33333333-3333-3333-3333-333333333333"
#   }
# }
