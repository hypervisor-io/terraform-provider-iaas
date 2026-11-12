# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## Unreleased

### Added / Changed

- `iaas_microvm` gains `security_group_ids` (a set, optional and computed): security groups owned by the account that apply to the whole sandbox and are in force before it can receive traffic. Changing the set attaches and detaches groups in place; the set is read back from the API. The per-NIC attribute still works, but groups are per sandbox: every network entry must name the same set (a different set is a 422 `security_groups_differ_per_nic`). `iaas_security_group` gains `sandbox_ids` (attach and detach sandboxes in place). Needs a Master with the sandbox security group attach routes.

- `iaas_microvm_catalog` gains optional `image_id` and `image_version_id`: each location then lists only the plans whose disk holds that kit's image. Kits (in this data source and `iaas_microvm_images`) gain the computed `required_disk_gib` (null when unknown; unknown or 2 GiB or less excludes no plan, matching the node). `iaas_microvm.plan_id` documents the Master's early refusal of a plan that is too small (HTTP 422, reason `plan_disk_too_small`; nothing is created) and that an omitted plan is now the smallest one that holds the kit.
- New resource `iaas_sandbox_volume` and data source `iaas_sandbox_volumes`: persistent data volumes for sandboxes (`name`,
  `size_gib` 1-1024, `location_id`). A volume keeps its data when a sandbox stops, pauses or is deleted, lives on one node
  of its location and is billed per GiB-hour of its size. Every argument forces a new volume. Destroy waits until the
  volume is gone and fails with a hint while a sandbox still holds it (`volume_in_use`). A refresh drops a volume that is
  deleting and warns on `error`. Import by UUID reads everything. The resource is `POST /storage/volumes` with
  `for = "sandbox"`; `iaas_volume` stays the KVM block volume.
- `iaas_microvm` gains `volumes` (list of `volume_id`, `mount_path`, `read_only`, `noexec`, `delete_on_sandbox_delete`):
  the volumes the sandbox starts with, placed on the node that holds them. Create-time only: changing the list forces a
  new MicroVM, because a volume can be attached or detached only while the sandbox is stopped (the attach and detach
  endpoints are therefore excluded in the manifest). Read back from the platform: a platform default (no path chosen,
  flags off) never shows as a change; a volume attached or detached outside OpenTofu shows as a change of the list (use
  `lifecycle { ignore_changes = [volumes] }` to manage attachments yourself). Import reads every field.
- Client: `CreateSandboxVolume`, `DeleteSandboxVolume`, `ListSandboxVolumes`, `ListMicrovmVolumes`, `AttachMicrovmVolume`,
  `DetachMicrovmVolume`. The vendored API manifest is synced (three volume endpoints, four notes).
- `iaas_microvm.fast_start` (`allow`, `require`, `cold_only`): how a sandbox should start. `allow` (the platform default) takes the
  fastest start the location offers and otherwise starts normally, `cold_only` always starts normally, `require` never
  starts normally and fails with HTTP 429 reason `fast_start_unavailable` when the fast start cannot be had; nothing is
  created then, so the apply can be repeated. Create-time only: it is not read back, changing it forces a new MicroVM and
  an imported MicroVM carries none.
- New resource `iaas_microvm_snapshot` and data source `iaas_microvm_snapshots`: sandbox disk snapshots. Create takes
  the snapshot (`sandbox_id`, `name`, optional `description` and `consistency`), waits until it is `ready` and fails
  with the platform's `failure_reason` if it fails; it reports `paused_ms` and `capture_method` (how long the
  sandbox was paused and how the disk was copied), `used_bytes`, `billable_gib` and `price_gib_hour`. Every argument
  forces a new snapshot. A snapshot survives the deletion of its sandbox (`sandbox_id` is kept, no drift). A refresh
  drops a `failed` or `deleting` snapshot from state and warns on `unavailable`. Delete waits until the snapshot is
  gone and retries while a sandbox is still being created from it. Import by UUID.
- `iaas_microvm_snapshot` create: a refusal the location can name before taking the snapshot (it would pause a running sandbox
  longer than allowed, file systems cannot be quiesced, not enough free disk) now arrives immediately as the create
  error with its reason (for example `snapshot_requires_pause`, with the estimated pause and the limit); nothing is
  created, so applying again after pausing the sandbox works. No schema change. When the location cannot answer in time
  the snapshot is requested as before and the same reasons surface through the failed-snapshot error.
- `iaas_microvm` gains `snapshot_id` (start from a snapshot instead of an image: boots cold from a copy of the saved
  disk, in the snapshot's location) and `inherit_env`. Exactly one of `image_id` and `snapshot_id`; `image_id` is
  now Optional+Computed and the location may be omitted with `snapshot_id`. `snapshot_id` is read back from the
  platform (`source_snapshot_id`), so import shows no diff, and it stays set after the snapshot is destroyed.
- Client: `CreateMicrovmSnapshot`, `GetMicrovmSnapshot`, `ListMicrovmSnapshots`, `DeleteMicrovmSnapshot`. The vendored
  API manifest is synced (four snapshot endpoints, all covered).
- Client only: `MintMicrovmAccess` and `MintMicrovmPreview` mint the short-lived run API token and the preview
  link of a sandbox (`POST /microvm/vm/{id}/access` and `/preview`). Both answer a credential that expires, not
  declarative state, so there is no resource or data source and nothing is stored in state; the methods exist for
  the MCP server, which mints a token inside one tool call and then calls the node itself. The vendored API
  manifest marks both endpoints as excluded for OpenTofu with that reason.
- Client only: `ListMicrovmEgressPresets` lists the platform's ready-made outbound policies
  (`GET /microvm/egress/presets`). A preset is a starting document: copy its rules into `egress_policy` on
  `iaas_microvm`, where they stay visible in the plan. There is no data source on purpose: expanding the
  platform's list at plan time would change a plan whenever the platform edits a preset. The method exists for
  the MCP server. The vendored API manifest marks the endpoint as excluded for OpenTofu with that reason.
- Client only: `GetMicrovmEgressLog` reads a sandbox's outbound refusal log (`GET /microvm/vm/{id}/egress/log`:
  what the policy refused, with filters and paging). The log is operational data that changes every minute, not
  declarative state, so it has no resource or data source; the method exists for the MCP server, which builds on
  this client. The vendored API manifest marks the endpoint as excluded for OpenTofu with that reason.
- `iaas_microvm.egress_policy_state` can read `inactive`: the sandbox is killed or errored, the stored policy is
  kept for reading and is in force nowhere (the platform used to keep reporting `applied` for it). An apply that
  is waiting for the node when the sandbox ends stops at once with a warning instead of running into its
  timeout. Needs a platform version that reports the value; older ones never send it.
- `iaas_microvm` gains `egress_policy`, the sandbox's outbound network policy: `default_action` (`allow` or
  `deny`), optional `inbound_replies`, and `rules` (`action`, `target` as a host name, `*.` wildcard, address
  or CIDR network, optional `protocol`, `ports`, `comment`, `id`). A policy configured at create is sent with
  the create request, so the sandbox never runs without it. Later changes are made in place (the whole policy
  is replaced), and removing the attribute removes the policy. The apply waits for a running or paused sandbox
  until the node has confirmed the change and fails when the node refuses it or cannot enforce it; a stopped
  sandbox keeps the policy pending and starts with it. Two computed attributes, `egress_policy_state` and
  `egress_policy_generation`, show how far the node is. A policy changed outside OpenTofu is detected through
  the generation on the sandbox read and shows as a diff; values the platform only normalises (lower-cased
  names, a default protocol, assigned rule ids) do not. `terraform import` reads the stored policy in its
  shortest form. Against a platform without outbound policies a configured `egress_policy` fails the apply
  instead of leaving an unrestricted sandbox. Needs a platform version with the sandbox outbound policy API.
- `iaas_microvm` gains optional `metadata` (string map, changed in place through the new metadata endpoint). Sandbox create and kit build now send an `Idempotency-Key`, so a retried create is replayed by the platform instead of starting a second billed sandbox. The client gains `UpdateMicrovmMetadata`, `ListMicrovmsByMetadata` and `RemoveMicrovmTimeout`.
- `iaas_microvm` gains the read-only attributes `last_resume_mode` (`snapshot` or `fresh_boot`), `last_resume_reason` and `last_resumed_at`. A `fresh_boot` resume means the sandbox was restarted from disk and its in-memory state was not kept. All three are null until the first resume.
- `iaas_s3_bucket` destroy now waits for the asynchronous bucket delete (the API answers 202 `deleting`): it polls until the bucket is gone, fails with the platform's `delete_error` when the delete failed (the bucket still exists; run destroy again), and honours a new `timeouts { delete = ... }` block (default 10 minutes). A 409 `delete_in_progress` is treated as accepted.
- `iaas_s3_access_key` is now deleted server-side on `terraform destroy` (it was a state-only removal with a warning). The platform removes the key's credentials from its storage servers first and keeps the key, answering HTTP 502, when a storage server cannot be reached; destroy can then simply be repeated. A key that is already gone counts as deleted.

- `iaas_s3_bucket.name` is validated at plan time against the AWS S3 bucket naming rules the API now enforces (3-63 characters; lowercase letters, digits, dots, hyphens; begin and end with a letter or digit; no adjacent dots; not an IP address; no `xn--`, `sthree-` or `amzn-s3-demo-` prefix; no `-s3alias`, `--ol-s3`, `.mrap`, `--x-s3` or `--table-s3` suffix). Names are unique across the whole platform, deleted buckets included; a taken name is refused by the API at apply time. The naming check runs only when a bucket is created or its name changes (a replacement); an existing bucket whose accepted name no longer meets the rules, such as `sthree-assets`, keeps planning clean while its name is unchanged.
- `iaas_managed_database` gains optional `parameter_group_id` (a parameter group of the account with the
  same engine), sent on create and changed in place through the apply-parameter-group endpoint. The API
  cannot detach a group, so removing the argument leaves the applied group in place.
- `iaas_autoscaling_group` gains optional `user_scripts` (user script ids owned by the account, run via
  cloud-init on every new instance). Create-only like `ssh_keys`: changing it forces a new group.
- `iaas_alert_rule` documentation names the monitoring catalog ids (`cpu`, `memory`, `disk`, ...) as the
  canonical `metric` values and the legacy `cpu_pct`, `mem_pct`, `memory_pct`, `disk_pct` as accepted
  aliases. The stored value is unchanged, so existing configurations stay stable.
- `iaas_security_group`: a rule add that the API answers with success plus the warning
  `config_push_failed` (rule saved, node push failed) is shown as a warning, not an error; the rule is
  tracked and is not posted again. Refusals (HTTP 422) are errors with the API message.
- New resource `iaas_vpn_ipsec_connection`: an IPsec site-to-site connection (IKEv2, or IKEv1 with the
  compat preset; pre-shared key) of a VPN gateway, a child of `iaas_vpn_gateway` like `iaas_vpn_peer`
  (import `<gateway_id>/<connection_id>`). The pre-shared key `psk` is Sensitive and write-only (the
  API never returns it): changing it updates the key in place and never replaces the connection, a key
  changed outside Terraform is detected through the platform's key version and shows as an in-place
  update back to the configured value, and after an import the next apply sends it. Writes are applied
  to the gateway asynchronously, so create and update wait until `apply_state` is `applied` (a gateway
  that reports `failed` fails the apply with its code), delete waits until the gateway has confirmed the
  removal (a removed connection keeps its networks reserved until then, so a replacement with the same
  `remote_ts` needs it), and the live tunnel (`sa_state`) is exposed but never waited for. Create is sent
  in exactly one attempt; an uncertain answer (5xx, transport error) is never replayed and is never
  adopted either (the API has no request token, so a same-name connection could belong to another
  writer): the error names the connection that now exists and gives the exact `terraform import`
  command. Delete keeps waiting for the gateway after a replayed DELETE meets 404, and the apply wait
  fails fast when the gateway's IPsec install fails. Selector lists must be canonical, non-overlapping
  and (remote) outside the reserved ranges; empty or padded optional text is refused at plan time;
  cross-field checks run only on explicitly configured values; a 409 `ipsec_installing` (the gateway is
  still installing its IPsec packages, for example for a sibling connection created in parallel) is a
  definite refusal and is waited out within the create or update timeout. Plan-time validation mirrors
  the server grammars (name, key shape without ever echoing the key, `/8` to `/32` remote networks,
  IKEv1 needs the compat preset, custom proposals, `any` identity for a responder only, child lifetime
  not above the IKE lifetime). The computed `remote_settings` attribute is the JSON document of values
  to enter on the other device (never the key). `restart` is an imperative action and is not modelled.
  The `client` package gains the IPsec calls (`ListVpnIpsecConnections`, `GetVpnIpsecConnection`,
  `CreateVpnIpsecConnection`, `UpdateVpnIpsecConnection`, `DeleteVpnIpsecConnection`,
  `RestartVpnIpsecConnection`, `GetVpnIpsecRemoteSettings`, `GetVpnIpsecRemoteSettingsText`) that the MCP server shares.

- `iaas_microvm_images` and `iaas_microvm_catalog`: each `suggested_secrets` entry now also
  exposes the computed `provider`, `placeholder`, `icon` and `role` attributes (null when the
  Master or the kit does not set them).
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
