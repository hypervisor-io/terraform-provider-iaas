# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## Unreleased

### Added / Changed

- MicroVM v3 network/SSH/catalog contract (spec `/spec/microvm/v3`):
  - `iaas_microvm` gains `ssh_key_ids` (list of account SSH key UUIDs, installed to
    every user's `authorized_keys` at first boot - changing it forces a new
    resource), a computed `ssh` command string for the default-route interface,
    and `location_id` (canonical replacement for `hypervisor_group_id`, same
    deprecation/`ExactlyOneOf` shape as the other resources above). The `network`
    block no longer accepts `kind = "isolated"` (public and vpc only); a public
    entry's `subnet_id` is optional (the platform auto-assigns a subnet with free
    capacity when omitted) and gains an optional `static_ip_id` (one of the
    account's allocated static IPs in the microVM's location); the computed
    `interfaces` list gains a `static_ip {id, ip}` object per entry.
  - `iaas_microvm_image` gains `location_id` (same deprecation shape) and
    computed `os_id`/`os_family`/`os_name`/`os_version`/`os_codename`/`arch`/
    `features` (a Dockerfile build's catalog base OS metadata). `base_image_id`
    is required when `source_kind = "dockerfile"` (validated at plan time via
    `ValidateConfig`, mirroring the API's `required_if` rule).
  - New `iaas_microvm_settings` resource: manages the account's default microVM
    network (used by any `iaas_microvm`/E2B create that omits `network`) via
    `GET`/`PUT /microvm/settings`. It is a per-account singleton (fixed `id =
    "default"`); destroying it clears the default (`PUT` with `default_network =
    null`).
  - Fixed a latent bug in `iaas_microvm_image`'s `Read`/`Update`: the client's
    `GetMicrovmImage` returns the bare SHOW envelope
    (`{image,versions,microvms_count}`), which was passed to the state mapper
    unwrapped - every field silently fell back to the prior state instead of
    refreshing. The resource now unwraps the `image` key first.

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
