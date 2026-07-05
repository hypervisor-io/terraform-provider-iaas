data "iaas_kubernetes_vpc" "prod" {
  name = "prod-vpc"
}

data "iaas_kubernetes_subnet" "cp" {
  vpc_id = data.iaas_kubernetes_vpc.prod.id
  name   = "cp-subnet"
  type   = "private"
}
