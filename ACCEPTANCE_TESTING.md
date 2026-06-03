# Acceptance testing (live staging — manual gate)

> The full, canonical runbook is the tfplugindocs guide
> **[`docs/guides/acceptance-testing.md`](docs/guides/acceptance-testing.md)**
> (rendered from `templates/guides/acceptance-testing.md.tmpl` by `make docs`).
> This top-level copy is the quick operator reference; the guide has the complete
> per-resource env-var table, the tier checklist, and the watch-items.

The `TestAcc*` tests are a **manual gate**. They run only with `TF_ACC=1`, against
a **real staging panel** with a **real IP-locked Bearer token**, from a
**static-IP host**. They cannot run in CI (the token is locked to a fixed egress
IP; CI has a dynamic IP and no panel). CI runs only the client unit tests, the
mock-backed `TestUnit*` lifecycle tests, `go vet`, `gofmt`, and `tofu validate`.

Without `TF_ACC`, every `TestAcc*` auto-skips (the terraform-plugin-testing
harness no-ops `resource.Test`). Verify with:

```sh
go test ./internal/resources/... ./internal/datasources/... -run TestAcc -count=1
# → every TestAcc* prints --- SKIP; packages report ok; nothing FAILs.
```

## Coverage

One `TestAcc*` per registered resource (**35**) and per data source (**10**) =
**45** acceptance tests. The data-source acc tests live in
`internal/datasources/acc_test.go`.

## Prerequisites (summary)

1. A **static-IP host** (workstation / bastion / static or self-hosted CI runner).
2. An **IP-locked Bearer token registered from that host's egress IP**
   (`curl https://ifconfig.me` to confirm the IP). The token only authenticates
   from its registered IP.
3. A **reachable staging panel** (its `/api` base URL).
4. **`billing.enabled`** on staging for billing-gated resources: `iaas_volume`,
   `iaas_static_ip`, `iaas_managed_database`, `iaas_db_backup_policy` (and
   `iaas_db_replica`).
5. **OpenTofu or Terraform** on `PATH` (or `TF_ACC_TERRAFORM_PATH`); the testing
   harness shells out to it.

## Environment variables

Always required: `TF_ACC=1`, `IAAS_API_ENDPOINT` (e.g.
`https://panel.staging.example.com/api`), `IAAS_API_TOKEN` (the IP-locked token).
Optionally `TF_ACC_TERRAFORM_PATH`, `IAAS_INSTANCE_POLL_INTERVAL`.

Per-resource / per-data-source extra IDs (tests skip cleanly when unset) — see the
[full table in the guide](docs/guides/acceptance-testing.md#extra-per-resource--per-data-source-ids).
In brief: `IAAS_TEST_HG_ID`, `IAAS_TEST_VOLUME_PLAN_ID`, `IAAS_TEST_VOLUME_ID`,
`IAAS_TEST_STATIC_IP_ID`, `IAAS_TEST_LB_LOCATION_ID`, `IAAS_TEST_LB_PLAN_ID`,
`IAAS_TEST_LB_ID`, `IAAS_TEST_VPC_ID`, `IAAS_TEST_VPC_SUBNET_ID`,
`IAAS_TEST_VPNGW_PLAN_ID`, `IAAS_TEST_VPN_GATEWAY_ID`, `IAAS_TEST_VPN_PEER_ID`,
`IAAS_TEST_DB_PLAN_ID`, `IAAS_TEST_DB_VPC_ID`, `IAAS_TEST_DB_VPC_SUBNET_ID`,
`IAAS_TEST_DB_PRIMARY_ID`, `IAAS_TEST_DB_REPLICA_PLAN_ID`, `IAAS_TEST_S3_PLAN_ID`,
`IAAS_TEST_S3_SERVER_ID`, the `IAAS_TEST_K8S_*` set
(`HG_ID`/`VPC_ID`/`CP_SUBNET_ID`/`WORKER_SUBNET_ID`/`VERSION_ID`/`CP_PLAN_ID`/`WORKER_PLAN_ID`/`CP_LB_PLAN_ID`/`CLUSTER_ID`),
and the data-source filters `IAAS_TEST_LOCATION_NAME`, `IAAS_TEST_PLAN_LOCATION_ID`,
`IAAS_TEST_PLAN_NAME`, `IAAS_TEST_IMAGE_NAME`, `IAAS_TEST_ISO_NAME`,
`IAAS_TEST_K8S_VERSION_NAME`, `IAAS_TEST_K8S_REGION_NAME`, `IAAS_TEST_K8S_PLAN_KIND`,
`IAAS_TEST_K8S_PLAN_NAME`.

Obtain catalog IDs via the catalog data sources (`iaas_location`, `iaas_plan`,
`iaas_image`, `iaas_iso`, `iaas_kubernetes_{version,region,plan}`) or from the panel.

### Placeholder skeletons that need a hand-edited config

These always `t.Skip` even with `TF_ACC` — edit the inline HCL `config` (and remove
the `t.Skip` line) to point at a real parent before running:
`TestAccInstance_basic` (REPLACE-WITH-…-UUID placeholders), the LB children
(`TestAccLB{Backend,Target,Frontend,RoutingRule,Certificate}_basic`), DNS
(`TestAccDNS{Zone,RecordSet,Record}_basic`), and autoscaling
(`TestAccAutoscaling{Group,Policy}_basic`). Their mock `TestUnit*` lifecycles run
in CI and prove the CRUD/import wiring.

## Commands

```sh
# Full suite (instance ~30m, k8s ~45m → use a long timeout)
TF_ACC=1 IAAS_API_ENDPOINT=... IAAS_API_TOKEN=... \
  go test ./internal/resources/... ./internal/datasources/... -run TestAcc -v -timeout 120m

# Tier 1 golden first
TF_ACC=1 go test ./internal/resources/ -run \
  'TestAccSSHKey_basic|TestAccVPC_basic|TestAccVPCSubnet_basic|TestAccInstance_basic' -v -timeout 60m
```

Run tier-by-tier (networking, then data/storage, then k8s) per the
[guide's command section](docs/guides/acceptance-testing.md#commands).

## Watch-items to validate on staging

Confirm each (detail in the
[guide](docs/guides/acceptance-testing.md#known-watch-items-to-validate-against-real-staging)):

1. VPC `description` empty-vs-null round-trip (no spurious inconsistent-result).
2. `iaas_db_parameter_group` suffix-param rejection (only suffix-free params).
3. `iaas_managed_database` / `iaas_db_backup_policy` write-only credential
   preservation across no-op apply + import.
4. `iaas_s3_access_key` secret shown-once → persisted, no-op-stable, import-clean.
5. Named-key paginators (LB / managed DB / k8s clusters) are page-1-only — fine
   for the small staging set; revisit if a future list spans >1 page.
6. Async convergence within timeouts; a failed create still leaves a destroyable
   resource (id persisted before the wait).
7. Sensitive non-leakage: `vnc_password`, S3 secret, kubeconfig, autoscaler
   manifest, vpn_peer_config, LB/notification secrets — Sensitive and absent from
   plan/apply output.

## Status

Runbook delivered. **The live staging `TF_ACC=1` pass has NOT been executed** (no
panel / no IP-locked token / dynamic IP in the build environment). It remains a
manual gate for an operator on a static-IP host; record outcomes in the guide's
[results checklist](docs/guides/acceptance-testing.md#results-checklist).
