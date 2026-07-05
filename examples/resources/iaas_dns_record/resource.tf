# A DNS record is a single value within a record set (e.g. one IP for an A set).
# Its value format is validated server-side against the parent set's type. An
# optional health_check attaches an active health check; an unhealthy record is
# withheld from resolution (fail-open if all records in the set are unhealthy).

resource "iaas_dns_zone" "example" {
  name = "corp.internal"
}

resource "iaas_dns_record_set" "www" {
  zone_id        = iaas_dns_zone.example.id
  name           = "www"
  type           = "A"
  routing_policy = "weighted"
  ttl            = 300
}

resource "iaas_dns_record" "www_a1" {
  zone_id       = iaas_dns_zone.example.id
  record_set_id = iaas_dns_record_set.www.id

  # The record value - an IPv4 address for an A set. Updatable in place.
  value = "10.0.0.10"

  # Required for the "weighted" policy; ignored otherwise (1-255).
  weight = 60

  # Optional active health check (single nested attribute, assigned with `= {}`).
  # Remove the block to detach the check.
  health_check = {
    type            = "http"
    port            = 80
    path            = "/healthz"
    expected_status = 200
    interval        = 30
    timeout         = 5
  }
}

resource "iaas_dns_record" "www_a2" {
  zone_id       = iaas_dns_zone.example.id
  record_set_id = iaas_dns_record_set.www.id
  value         = "10.0.0.11"
  weight        = 40
}
