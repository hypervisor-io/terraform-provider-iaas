# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## Unreleased

### Added / Changed

- `iaas_volume` attach, detach, resize and delete are now confirmed operations. The
  platform accepts these requests and the node performs them; the resource waits until
  the volume reports the new attachment, plan and size (or reads 404 after a delete)
  and no longer trusts the accepted response. A node failure, a refusal (now shown with
  its `reason` code) or a timeout fails the apply and keeps the volume in its previous
  state; after a failed update the state is refreshed from the API so the next plan
  shows exactly what is still missing. The `update` and `delete` timeouts now bound
  real waits (update defaults to 30m because an update can chain resize, detach and
  attach, and the platform enforces a short cooldown between operations on one volume,
  which the resource waits out). Destroying an attached volume detaches it first. The
  shared wait lives in the new `volumeop` package, which the MCP server uses too.
  The four volume commands are sent in exactly one attempt (a 5xx or transport error
  is resolved by reading the volume back, never by replaying the command), a busy
  refusal is waited out even before the platform publishes the pending marker, a
  refusal that raced a completion already in the requested state counts as done,
  destroy waits for pending volume work before deciding whether to detach, and the
  create timeout is one deadline for provisioning plus the optional attach.
  `client.APIError` gains a `Reason` field (the API's typed refusal code), shown in
  `Error()` and in every resource's error diagnostics.

- Volume operation gate refusals are read from their typed answer (Master 3.3.5): busy is
  HTTP 409 reason `volume_busy`, the per-volume cooldown is HTTP 429 reason
  `volume_cooldown` with a `Retry-After` header, and the account in-flight limit is HTTP
  429 reason `account_inflight_limit`. `volumeop` waits out the cooldown for the time
  `Retry-After` names (bounded to 300 seconds, and ending at once with a still-pending
  error when the wait cannot fit the deadline) and backs off on busy and in-flight
  refusals; any other typed reason is a definite refusal even when its text mentions
  "busy". Older masters' 422 `volume_operation_rejected` answers are still recognised from
  their message text. `client.APIError` gains `RetryAfter`. `iaas_volume_snapshot` create
  (and the client's volume backup create) is now sent in exactly one attempt and waits
  out the same gate refusals through the new `volumeop.Admit`; an uncertain answer (5xx,
  transport error) is resolved by one read-back on a fresh 30-second context, never by
  sending the create again, and adopts only a snapshot of that name that did not exist
  before the send (exactly one; several are reported as ambiguous). Because the platform
  does not enforce unique snapshot names per volume and the new snapshot is found by its
  name, `iaas_volume_snapshot` (and the MCP snapshot tool) now refuse a create whose name
  is already used on the volume, before anything is sent: the resource can no longer
  adopt, and later destroy, a snapshot it did not create. Empty, all-whitespace,
  leading/trailing-whitespace and literal `"0"` snapshot names are refused before any
  API read or send, with the same rule at provider plan time: the platform would trim
  or replace these names and the recorded snapshot could not be found by its configured
  name. The set of same-name snapshots seen before a send is refreshed before every
  repeated send, and the
  new snapshot's id is resolved as the single same-name snapshot not in that set, so one
  made by someone else during an admission wait is never taken for ours. Admission, id
  resolution and the readiness wait now share one create timeout instead of each getting
  a full one.

- `iaas_certificate`'s `name`, `certificate`, `private_key` and `chain` attributes are no
  longer `RequiresReplace`. Changing any of them now rotates the certificate material IN
  PLACE via `PUT /certificate/{id}`, preserving the resource's `id` (and therefore every
  `iaas_lb_frontend`'s `certificate_ids`/`ssl_certificate_id` reference to it) instead of
  destroying and recreating the resource. The computed `domain`/`san_domains`/`expires_at`/
  `fingerprint_sha256`/`status` attributes now refresh on every such update. A certificate
  issued via Let's Encrypt cannot be rotated this way (it renews automatically); attempting
  to change one now fails with a dedicated, actionable error instead of the previous
  destroy/recreate behavior. Every load balancer that references the certificate is
  best-effort re-synced server-side; a failed re-sync for an individual load balancer
  surfaces as a Terraform warning, never an error (the certificate itself is already
  updated by that point).

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
