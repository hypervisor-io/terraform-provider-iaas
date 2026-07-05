---
page_title: "Running the acceptance tests"
subcategory: "Guides"
description: |-
  How an operator on a static-IP host runs the live TF_ACC acceptance-test suite against a real staging panel, and records the results.
---

# Running the acceptance tests (manual staging gate)

The provider ships two layers of tests:

| Layer | Where | Runs in CI? | What it proves |
|-------|-------|-------------|----------------|
| Client unit tests (`net/http/httptest`) | `internal/client/*_test.go` | Yes | The HTTP client speaks the real envelopes/verbs/paths. |
| Mock-backed lifecycle tests (`TestUnit*`, `resource.UnitTest`) | `internal/{resources,datasources}/*_test.go` | Yes (needs `tofu`/`terraform` on PATH) | Create→read→import→update→delete against canned responses; request bodies asserted. |
| **Live acceptance tests (`TestAcc*`, `resource.Test`)** | `internal/{resources,datasources}/*_test.go` | **No - manual gate** | The provider works end-to-end against a real panel. |

The `TestAcc*` tests are a **manual gate**: they only execute when `TF_ACC=1`
is set, and they talk to a **real staging panel** with a **real IP-locked Bearer
token**. They cannot run in CI (the panel is private and the token is locked to a
static egress IP - see below), so a human runs them from a static-IP host and
records the outcome in the [results checklist](#results-checklist).

-> Without `TF_ACC` every `TestAcc*` no-ops (the terraform-plugin-testing harness
skips `resource.Test` automatically). This is verified in CI - see
[Verifying the suite auto-skips](#verifying-the-suite-auto-skips).

## Prerequisites

1. **A static-IP host.** A workstation, bastion, or a static/self-hosted CI
   runner whose outbound (egress) IP does not change. The provider's token is
   IP-locked, so a dynamic-IP host (laptop on DHCP/VPN, GitHub-hosted runner)
   will get `401/403` on every call.

2. **An IP-locked Bearer token registered FROM that host's egress IP.**
   In the panel, create a user API token *while connected from the host that will
   run the tests* (or register the host's egress IP against the token). The token
   only authenticates from the IP it was registered with - the same value goes in
   `IAAS_API_TOKEN`. Confirm the host's egress IP first, e.g. `curl https://ifconfig.me`.

3. **A reachable staging panel.** The `/api` base URL goes in
   `IAAS_API_ENDPOINT` (include the `/api` suffix). The token's user must have the
   scopes/permissions for the resources under test (a full-access, non-subuser
   token is simplest).

4. **`billing.enabled` for billing-gated resources.** The following resources are
   gated behind billing being enabled on the panel and return `403` otherwise:
   **`iaas_volume`, `iaas_static_ip`, `iaas_managed_database`,
   `iaas_db_backup_policy`** (and `iaas_db_replica`, which builds on a managed
   database). Enable billing on staging before running their tiers.

5. **An OpenTofu or Terraform binary.** `terraform-plugin-testing` shells out to a
   real CLI. Put `tofu` (or `terraform`) on `PATH`, **or** point
   `TF_ACC_TERRAFORM_PATH` at the binary. (OpenTofu ≥ 1.6 / Terraform ≥ 1.0.)

## Environment variables

### Always required

| Variable | Value |
|----------|-------|
| `TF_ACC` | `1` (turns the manual gate on) |
| `IAAS_API_ENDPOINT` | Staging API base URL, e.g. `https://panel.staging.example.com/api` |
| `IAAS_API_TOKEN` | The IP-locked Bearer token registered from this host's egress IP |
| `TF_ACC_TERRAFORM_PATH` | Path to `tofu`/`terraform` (omit if it is on `PATH`) |

Optional tuning: `IAAS_INSTANCE_POLL_INTERVAL` (shortens async poll interval in
tests; e.g. `5s`).

### Extra per-resource / per-data-source IDs

Many acceptance tests need a pre-existing entity ID (a parent, a catalog entry,
or a billing-enabled location) that the test does not create itself. Supply these
from the panel or via the catalog data sources. **Tests skip cleanly when their
extra var is unset**, so you can run the suite incrementally.

Obtain catalog IDs the same way the provider does: run a tiny `tofu apply` of the
matching catalog data source (`iaas_location`, `iaas_plan`, `iaas_image`,
`iaas_iso`, `iaas_kubernetes_{version,region,plan}`) and read its `id` output, or
copy the UUID from the panel URL/API.

| Test (resource / DS) | TestAcc | Extra env vars | How to obtain |
|----------------------|---------|----------------|---------------|
| `iaas_volume` | `TestAccVolume_basic` | `IAAS_TEST_VOLUME_PLAN_ID`, `IAAS_TEST_HG_ID` | volume-enabled hypervisor-group + an enabled volume plan |
| `iaas_volume_snapshot` | `TestAccVolumeSnapshot_basic` | `IAAS_TEST_VOLUME_ID` | UUID of an existing, available `iaas_volume` |
| `iaas_static_ip` | `TestAccStaticIP_basic` | `IAAS_TEST_STATIC_IP_ID`, `IAAS_TEST_HG_ID` | an available (unallocated) IP + its hypervisor group |
| `iaas_load_balancer` | `TestAccLoadBalancer_basic` | `IAAS_TEST_LB_LOCATION_ID`, `IAAS_TEST_LB_PLAN_ID` | a hypervisor group with `lb_enabled=1` + an enabled `lb_plan` |
| `iaas_nat_gateway` | `TestAccNatGateway_basic` | `IAAS_TEST_VPC_ID` | a VPC in a natgw-enabled location with a private subnet |
| `iaas_vpn_gateway` | `TestAccVpnGateway_basic` | `IAAS_TEST_VPC_ID`, `IAAS_TEST_VPC_SUBNET_ID`, `IAAS_TEST_VPNGW_PLAN_ID` | VPC + a public subnet with a free IP + an enabled VPN-gateway plan |
| `iaas_vpn_peer` | `TestAccVpnPeer_basic` | `IAAS_TEST_VPN_GATEWAY_ID` | UUID of an active `iaas_vpn_gateway` |
| `iaas_managed_database` | `TestAccManagedDatabase_basic` | `IAAS_TEST_DB_PLAN_ID`, `IAAS_TEST_DB_VPC_ID`, `IAAS_TEST_DB_VPC_SUBNET_ID` | enabled db plan + VPC in a db-enabled location + a public subnet with a free IP |
| `iaas_db_replica` | `TestAccDBReplica_basic` | `IAAS_TEST_DB_PRIMARY_ID`, `IAAS_TEST_DB_REPLICA_PLAN_ID`, `IAAS_TEST_DB_VPC_SUBNET_ID` | active primary DB + a db plan (storage ≥ primary's) + a subnet in the primary's VPC |
| `iaas_s3_bucket` | `TestAccS3Bucket_basic` | `IAAS_TEST_S3_PLAN_ID`, `IAAS_TEST_S3_SERVER_ID` | an enabled S3 plan + an S3 server |
| `iaas_kubernetes_cluster` | `TestAccKubernetesCluster_basic` | `IAAS_TEST_K8S_HG_ID`, `IAAS_TEST_K8S_VPC_ID`, `IAAS_TEST_K8S_CP_SUBNET_ID`, `IAAS_TEST_K8S_WORKER_SUBNET_ID`, `IAAS_TEST_K8S_VERSION_ID`, `IAAS_TEST_K8S_CP_PLAN_ID`, `IAAS_TEST_K8S_WORKER_PLAN_ID`, `IAAS_TEST_K8S_CP_LB_PLAN_ID` | k8s-eligible region/HG, VPC, cp+worker subnets, k8s version, cp+worker instance plans, cp LB plan (use `iaas_kubernetes_{region,version,plan}` data sources) |
| `iaas_kubernetes_node_pool` | `TestAccKubernetesNodePool_basic` | `IAAS_TEST_K8S_CLUSTER_ID`, `IAAS_TEST_K8S_WORKER_PLAN_ID` | an active cluster + a worker instance plan |
| `iaas_location` (DS) | `TestAccLocation_basic` | `IAAS_TEST_LOCATION_NAME` | slug or display name of a location |
| `iaas_plan` (DS) | `TestAccPlan_basic` | `IAAS_TEST_PLAN_LOCATION_ID`, `IAAS_TEST_PLAN_NAME` | a location UUID + a plan name in it |
| `iaas_image` (DS) | `TestAccImage_basic` | `IAAS_TEST_IMAGE_NAME` | name of an available image (e.g. `Ubuntu 24.04`) |
| `iaas_iso` (DS) | `TestAccISO_basic` | `IAAS_TEST_ISO_NAME` | name of an available ISO (e.g. `AlmaLinux 9`) |
| `iaas_kubernetes_version` (DS) | `TestAccKubernetesVersion_basic` | `IAAS_TEST_K8S_VERSION_NAME` | semantic version of an active k8s version (e.g. `1.31.4`) |
| `iaas_kubernetes_region` (DS) | `TestAccKubernetesRegion_basic` | `IAAS_TEST_K8S_REGION_NAME` | name or slug of a k8s-eligible region |
| `iaas_kubernetes_plan` (DS) | `TestAccKubernetesPlan_basic` | `IAAS_TEST_K8S_PLAN_KIND` (`worker`\|`cp`\|`lb`), `IAAS_TEST_K8S_PLAN_NAME` | a plan name of that kind |
| `iaas_kubernetes_kubeconfig` (DS) | `TestAccKubernetesKubeconfig_basic` | `IAAS_TEST_K8S_CLUSTER_ID` | UUID of a **running** cluster |
| `iaas_kubernetes_autoscaler_manifest` (DS) | `TestAccKubernetesAutoscalerManifest_basic` | `IAAS_TEST_K8S_CLUSTER_ID` | UUID of a running cluster with worker autoscaling enabled |
| `iaas_vpn_peer_config` (DS) | `TestAccVpnPeerConfig_basic` | `IAAS_TEST_VPN_GATEWAY_ID`, `IAAS_TEST_VPN_PEER_ID` | active gateway + a `road_warrior` peer on it |

-> The remaining resources (`iaas_ssh_key`, `iaas_vpc`, `iaas_vpc_subnet`,
`iaas_project`, `iaas_security_group`, `iaas_ip_set`, `iaas_s3_access_key`,
`iaas_db_parameter_group`, `iaas_alert_rule`, `iaas_notification_channel`,
`iaas_instance_backup_policy`, `iaas_db_backup_policy`) build everything they need
themselves and require **no extra env var** beyond the three always-required ones.

### Tests that need a hand-edited config (placeholder skeletons)

A few `TestAcc*` are **placeholder skeletons that always `t.Skip`**, even with
`TF_ACC=1`. They reference a parent whose ID is hard to obtain generically, so the
operator must **edit the test's inline HCL `config` to point at a real parent**
(and remove the leading `t.Skip(...)` line) before running them:

- `iaas_instance` - `TestAccInstance_basic`: the config has
  `REPLACE-WITH-A-LOCATION-UUID` / `...PLAN-UUID` / `...IMAGE-UUID` placeholders.
  Substitute real catalog UUIDs (from the `iaas_location`/`iaas_plan`/`iaas_image`
  data sources).
- LB children - `TestAccLBBackend_basic`, `TestAccLBTarget_basic`,
  `TestAccLBFrontend_basic`, `TestAccLBRoutingRule_basic`,
  `TestAccLBCertificate_basic`: each needs a real `load_balancer` /
  `backend` / `frontend` id wired into its config.
- DNS - `TestAccDNSZone_basic`, `TestAccDNSRecordSet_basic`,
  `TestAccDNSRecord_basic`: zone, then record-set, then record (chain the ids).
- Autoscaling - `TestAccAutoscalingGroup_basic`, `TestAccAutoscalingPolicy_basic`:
  need a real instance/template to scale.

~> These do not run unattended. Treat them as "edit then run" during the staging
pass and note in the checklist whether they were exercised manually. The
mock-backed `TestUnit*` lifecycle for each of these resources DOES run in CI and
proves the CRUD/import wiring against canned responses.

## Commands

Run from the provider repo root, on the static-IP host, with the env set.

```sh
# Full suite (all tiers). Use a long timeout: instance ~30m, k8s ~45m.
TF_ACC=1 \
IAAS_API_ENDPOINT=... IAAS_API_TOKEN=... \
go test ./internal/resources/... ./internal/datasources/... \
  -run TestAcc -v -timeout 120m
```

Run it tier by tier (recommended - fail fast, lower blast radius):

```sh
# Tier 1 - golden path first (cheap, no billing). Edit TestAccInstance_basic config first.
TF_ACC=1 go test ./internal/resources/ -run \
  'TestAccSSHKey_basic|TestAccVPC_basic|TestAccVPCSubnet_basic|TestAccInstance_basic' \
  -v -timeout 60m

# Tier 1 remainder + catalog data sources
TF_ACC=1 go test ./internal/resources/ ./internal/datasources/ -run \
  'TestAccProject_basic|TestAccSecurityGroup_basic|TestAccIPSet_basic|TestAccVolume_basic|TestAccVolumeSnapshot_basic|TestAccStaticIP_basic|TestAccLocation_basic|TestAccPlan_basic|TestAccImage_basic|TestAccISO_basic' \
  -v -timeout 60m

# Tier 2 - networking (LB, NAT, VPN, DNS). LB-children + DNS need hand-edited configs.
TF_ACC=1 go test ./internal/resources/ ./internal/datasources/ -run \
  'TestAccLoadBalancer_basic|TestAccLB|TestAccNatGateway_basic|TestAccVpnGateway_basic|TestAccVpnPeer_basic|TestAccVpnPeerConfig_basic|TestAccDNS' \
  -v -timeout 60m

# Tier 3 - data/storage/observability
TF_ACC=1 go test ./internal/resources/ -run \
  'TestAccManagedDatabase_basic|TestAccDBReplica_basic|TestAccDBParameterGroup_basic|TestAccS3Bucket_basic|TestAccS3AccessKey_basic|TestAccInstanceBackupPolicy_basic|TestAccDBBackupPolicy_basic|TestAccNotificationChannel_basic|TestAccAlertRule_basic|TestAccAutoscaling' \
  -v -timeout 60m

# Tier 4 - Kubernetes (longest; ~45m to converge)
TF_ACC=1 go test ./internal/resources/ ./internal/datasources/ -run \
  'TestAccKubernetes' \
  -v -timeout 90m
```

A single test:

```sh
TF_ACC=1 go test ./internal/resources/ -run TestAccVPC_basic -v -timeout 30m
```

### Long-running timeouts

Async resources converge via a waiter; the per-test default timeout is generous
but the **`go test -timeout` must exceed the sum** of the tests in the run:

| Resource | Typical convergence |
|----------|---------------------|
| `iaas_instance` | up to ~30 min |
| `iaas_load_balancer` | up to ~15 min |
| `iaas_managed_database` | up to ~30 min |
| `iaas_kubernetes_cluster` | up to ~45 min |

## Results checklist

Fill in `pass` / `fail` / `skipped` and notes. Tiers mirror the build order.

### Tier 1 - golden + foundation

| Resource / DS | TestAcc | Result | Notes |
|---------------|---------|--------|-------|
| `iaas_ssh_key` | `TestAccSSHKey_basic` | | |
| `iaas_vpc` | `TestAccVPC_basic` | | check `description` null-vs-empty round-trip |
| `iaas_vpc_subnet` | `TestAccVPCSubnet_basic` | | server-mutable `used`/`free` not masking drift |
| `iaas_instance` | `TestAccInstance_basic` | | EDIT config UUIDs first; `vnc_password` must not leak |
| `iaas_project` | `TestAccProject_basic` | | |
| `iaas_security_group` | `TestAccSecurityGroup_basic` | | rules round-trip (comment preserved) |
| `iaas_ip_set` | `TestAccIPSet_basic` | | bulk-add drops `comment` - see watch-items |
| `iaas_volume` | `TestAccVolume_basic` | | billing.enabled |
| `iaas_volume_snapshot` | `TestAccVolumeSnapshot_basic` | | needs `IAAS_TEST_VOLUME_ID` |
| `iaas_static_ip` | `TestAccStaticIP_basic` | | billing.enabled |
| `iaas_location` (DS) | `TestAccLocation_basic` | | |
| `iaas_plan` (DS) | `TestAccPlan_basic` | | |
| `iaas_image` (DS) | `TestAccImage_basic` | | |
| `iaas_iso` (DS) | `TestAccISO_basic` | | |

### Tier 2 - networking

| Resource / DS | TestAcc | Result | Notes |
|---------------|---------|--------|-------|
| `iaas_nat_gateway` | `TestAccNatGateway_basic` | | one NAT gw per VPC |
| `iaas_load_balancer` | `TestAccLoadBalancer_basic` | | billing; async converge |
| `iaas_lb_backend` | `TestAccLBBackend_basic` | | placeholder - hand-edit config |
| `iaas_lb_target` | `TestAccLBTarget_basic` | | placeholder - hand-edit config |
| `iaas_lb_frontend` | `TestAccLBFrontend_basic` | | placeholder - hand-edit config |
| `iaas_lb_routing_rule` | `TestAccLBRoutingRule_basic` | | placeholder - hand-edit config |
| `iaas_lb_certificate` | `TestAccLBCertificate_basic` | | placeholder - hand-edit config |
| `iaas_vpn_gateway` | `TestAccVpnGateway_basic` | | async converge |
| `iaas_vpn_peer` | `TestAccVpnPeer_basic` | | needs `IAAS_TEST_VPN_GATEWAY_ID` |
| `iaas_vpn_peer_config` (DS) | `TestAccVpnPeerConfig_basic` | | `config` Sensitive, road_warrior only |
| `iaas_dns_zone` | `TestAccDNSZone_basic` | | placeholder - hand-edit config |
| `iaas_dns_record_set` | `TestAccDNSRecordSet_basic` | | placeholder - hand-edit config |
| `iaas_dns_record` | `TestAccDNSRecord_basic` | | placeholder - hand-edit config |

### Tier 3 - data / storage / observability

| Resource / DS | TestAcc | Result | Notes |
|---------------|---------|--------|-------|
| `iaas_managed_database` | `TestAccManagedDatabase_basic` | | billing; async; password preservation |
| `iaas_db_replica` | `TestAccDBReplica_basic` | | needs primary/plan/subnet |
| `iaas_db_parameter_group` | `TestAccDBParameterGroup_basic` | | suffix-param rejection (watch-item) |
| `iaas_s3_bucket` | `TestAccS3Bucket_basic` | | needs plan/server |
| `iaas_s3_access_key` | `TestAccS3AccessKey_basic` | | secret-key shown-once; Delete state-only |
| `iaas_instance_backup_policy` | `TestAccInstanceBackupPolicy_basic` | | |
| `iaas_db_backup_policy` | `TestAccDBBackupPolicy_basic` | | billing; credential preservation (watch-item) |
| `iaas_notification_channel` | `TestAccNotificationChannel_basic` | | hidden config write-only |
| `iaas_alert_rule` | `TestAccAlertRule_basic` | | |
| `iaas_autoscaling_group` | `TestAccAutoscalingGroup_basic` | | placeholder - hand-edit config |
| `iaas_autoscaling_policy` | `TestAccAutoscalingPolicy_basic` | | placeholder - hand-edit config |

### Tier 4 - Kubernetes

| Resource / DS | TestAcc | Result | Notes |
|---------------|---------|--------|-------|
| `iaas_kubernetes_cluster` | `TestAccKubernetesCluster_basic` | | ~45m; needs all `IAAS_TEST_K8S_*` |
| `iaas_kubernetes_node_pool` | `TestAccKubernetesNodePool_basic` | | needs cluster + worker plan |
| `iaas_kubernetes_version` (DS) | `TestAccKubernetesVersion_basic` | | |
| `iaas_kubernetes_region` (DS) | `TestAccKubernetesRegion_basic` | | |
| `iaas_kubernetes_plan` (DS) | `TestAccKubernetesPlan_basic` | | worker/cp/lb |
| `iaas_kubernetes_kubeconfig` (DS) | `TestAccKubernetesKubeconfig_basic` | | `kubeconfig` Sensitive; rotates per read |
| `iaas_kubernetes_autoscaler_manifest` (DS) | `TestAccKubernetesAutoscalerManifest_basic` | | `manifest` Sensitive; rotates per read |

## Known watch-items to validate against real staging

These are deviations and behaviours the build flagged for confirmation against a
live panel (from the build adaptation log). Verify each during the staging pass:

1. **VPC `description` empty-vs-null round-trip.** The API stores `null` when
   `description` is omitted. Apply a VPC with no description, then with `""`, then
   re-plan - confirm **no spurious "inconsistent result after apply"**. If staging
   returns `""` for an unset description, the mapping needs revisiting.
2. **`iaas_db_parameter_group` suffix-param rejection.** Only suffix-free
   parameters are supported. Confirm a suffix-bearing parameter is rejected (or
   documented as unsupported) and that a suffix-free set round-trips.
3. **`iaas_managed_database` / `iaas_db_backup_policy` write-only credential
   preservation.** The SHOW endpoint does not return the password/credential.
   Confirm an `apply` → `apply` (no change) does **not** drift or wipe the stored
   credential, and that import then plan is clean (ignored credential).
4. **`iaas_s3_access_key` secret shown-once.** The secret key is returned only on
   create. Confirm it lands in state, survives a no-op apply, and that an
   `import` then plan is clean (secret in `ImportStateVerifyIgnore`).
5. **Named-key paginators are page-1-only.** The list reads for
   load-balancers, managed databases, and k8s clusters currently read only the
   first page. For the small staging set this is fine, but if a future list/data
   source spans >1 page, confirm pagination is followed.
6. **Async convergence timeouts.** instance / load_balancer / managed_database /
   kubernetes_cluster all wait on a state poll. Confirm they reach ready within
   the documented timeouts and that a **failed** create still leaves a
   destroyable resource (id persisted before the wait).
7. **Sensitive non-leakage.** `iaas_instance.vnc_password`,
   `iaas_s3_access_key` secret, `iaas_kubernetes_kubeconfig.kubeconfig`,
   `iaas_kubernetes_autoscaler_manifest.manifest`, `iaas_vpn_peer_config.config`,
   and LB/notification secrets must be marked Sensitive and must **not** appear in
   plan/apply output or logs. Run with `-v` and grep the output to confirm no
   cleartext secret is printed.

## Verifying the suite auto-skips

CI must prove every `TestAcc*` no-ops without `TF_ACC`. Run **without** any of the
acc env vars set:

```sh
go test ./internal/resources/... ./internal/datasources/... -run TestAcc -count=1
```

Every `TestAcc*` reports `--- SKIP` (the harness skips `resource.Test` when
`TF_ACC` is unset) and the packages report `ok`. None must `FAIL`.

## CI note

These live acceptance tests **do not run in CI** - the CI host has a dynamic IP
and no staging panel/token, so an IP-locked token cannot authenticate. CI runs
**only** the client unit tests, the mock-backed `TestUnit*` lifecycle tests
(with `tofu` on PATH), `go vet`, `gofmt`, and `tofu validate` on the example
stack. The live pass is this manual gate, performed by an operator on a static-IP
host and recorded in the checklist above.
