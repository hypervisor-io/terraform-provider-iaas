data "iaas_kubernetes_vpc" "prod" {
  name = "prod-vpc"
}

# Use as vpc_id when declaring a cluster:
# resource "iaas_kubernetes_cluster" "c" {
#   vpc_id = data.iaas_kubernetes_vpc.prod.id
#   ...
# }
