# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## Unreleased

### Added / Changed

- The public name of a hypervisor group is now `location_id` on every resource and
  data source that previously accepted `hypervisor_group_id`
  (`iaas_image`, `iaas_kubernetes_vpc`, `iaas_kubernetes_region`,
  `iaas_autoscaling_group`, `iaas_kubernetes_cluster`, `iaas_load_balancer`,
  `iaas_managed_database`, `iaas_static_ip`, `iaas_volume`, `iaas_vpc`).
  `hypervisor_group_id` remains accepted as a deprecated alias and will be removed
  in the next release. The two are mutually exclusive - configuring both is a
  validation error - and where `hypervisor_group_id` was previously required,
  exactly one of the two must now be set. The provider always sends the
  canonical `location_id` to the API, and reads populate `location_id` from the
  API's `location_id` (falling back to `hypervisor_group_id` for hosts whose
  response has not yet been upgraded). Existing state that only has
  `hypervisor_group_id` continues to import and refresh with no diff.
