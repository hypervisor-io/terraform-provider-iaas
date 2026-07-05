# A DNS record set is a named group of records of one type inside a zone that
# share a routing policy and TTL. The (name, type) pair is unique within the zone.
# Add the actual values with iaas_dns_record resources that reference this set.

resource "iaas_dns_zone" "example" {
  name = "corp.internal"
}

resource "iaas_dns_record_set" "www" {
  # Parent zone id - part of the API path. Changing it forces a new resource.
  zone_id = iaas_dns_zone.example.id

  # Record name (the label left of the zone name). Updatable in place.
  name = "www"

  # Record type: A, AAAA, CNAME, TXT, or SRV. Updatable in place.
  type = "A"

  # Routing policy: simple, weighted, multivalue, or failover. CNAME cannot use
  # weighted or multivalue. Updatable in place.
  routing_policy = "weighted"

  # Time-to-live in seconds (30-86400). Updatable in place.
  ttl = 300
}
