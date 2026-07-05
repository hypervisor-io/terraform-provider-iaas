package client

import (
	"context"
	"fmt"
	"net/url"
)

// Admin API methods (verified against Master routes/api.php, prefix "api/v1",
// middleware "admin.api" = App\Http\Middleware\ApiAuthentication). The admin API
// is a DISTINCT surface from the user API (routes/user_api.php, prefix "api"):
// it is authenticated by an admin-scoped token that is IP-locked server-side
// (missing token -> 403, wrong token/IP -> 401). Because the shared transport's
// baseURL ends in "/api", every admin path here is prefixed "/v1" so it resolves
// to ".../api/v1/<path>".
//
// These methods exist ONLY for the MCP server's curated admin.* tool allowlist
// (spec 17 decision D3): admin READS plus a small set of reversible safe
// mutations (hypervisor maintenance toggle, rDNS-request approve/reject). They
// are additive - the OpenTofu provider does not call them - and deliberately do
// NOT cover destructive/irreversible admin routes (user deletion, hypervisor
// destroy, billing/credit mutations, bulk IP/subnet deletes, etc.).
//
// Envelope conventions on this surface (sampled from the controllers):
//   - INDEX handlers return a raw Laravel paginator ({data:[...], current_page,
//     last_page, ...}) or a bare array, so list methods use doList.
//   - SHOW handlers return the bare model ($model->toArray()) with no wrapper,
//     so get methods use doItem with key "" (the bare top-level object).
//   - The few safe mutations return {success,...} envelopes, checked by doItem.

// adminGet is the shared read helper: it fetches an admin path and returns the
// bare top-level object (no envelope key). Used by every admin get/stats method.
func (c *Client) adminGet(ctx context.Context, path string) (map[string]any, error) {
	return c.doItem(ctx, "GET", path, nil, "")
}

// ── instances ────────────────────────────────────────────────────────────────

func (c *Client) AdminListInstances(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instances", nil)
}

func (c *Client) AdminGetInstance(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/instance/"+url.PathEscape(id))
}

func (c *Client) AdminListUserInstances(ctx context.Context, userID string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instances/user/"+url.PathEscape(userID), nil)
}

func (c *Client) AdminGetInstanceBackups(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instance/"+url.PathEscape(id)+"/backups", nil)
}

func (c *Client) AdminGetInstanceIPs(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instance/"+url.PathEscape(id)+"/ips", nil)
}

func (c *Client) AdminGetInstanceMetrics(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/instance/"+url.PathEscape(id)+"/metrics")
}

func (c *Client) AdminGetInstanceDisks(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instance/"+url.PathEscape(id)+"/disks", nil)
}

// ── users ────────────────────────────────────────────────────────────────────

func (c *Client) AdminListUsers(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/users", nil)
}

func (c *Client) AdminGetUser(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/user/"+url.PathEscape(id))
}

func (c *Client) AdminListSessions(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/sessions", nil)
}

// ── tasks ────────────────────────────────────────────────────────────────────

func (c *Client) AdminListTasks(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/tasks", nil)
}

func (c *Client) AdminGetTask(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/task/"+url.PathEscape(id))
}

func (c *Client) AdminGetTaskLogs(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/task/"+url.PathEscape(id)+"/logs")
}

// ── hypervisors ──────────────────────────────────────────────────────────────

func (c *Client) AdminListHypervisors(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisors", nil)
}

func (c *Client) AdminGetHypervisor(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/"+url.PathEscape(id))
}

func (c *Client) AdminGetHypervisorMetrics(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/"+url.PathEscape(id)+"/metrics")
}

func (c *Client) AdminGetHypervisorInstanceStats(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/"+url.PathEscape(id)+"/instances/stats")
}

func (c *Client) AdminGetHypervisorIPv4Stats(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/"+url.PathEscape(id)+"/ipv4/stats")
}

// AdminSetHypervisorMaintenance is a SAFE, reversible mutation: it PATCHes ONLY
// the maintenance boolean on a hypervisor (via UpdateHypervisorRequest's
// "maintenance => sometimes|boolean"). It never touches any other field.
func (c *Client) AdminSetHypervisorMaintenance(ctx context.Context, id string, enabled bool, idemKey string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("AdminSetHypervisorMaintenance: empty id")
	}
	body := map[string]any{"maintenance": enabled}
	return c.doItemWithHeaders(ctx, "PATCH", "/v1/hypervisor/"+url.PathEscape(id), body, "", idemHeaders(idemKey))
}

// ── hypervisor groups ────────────────────────────────────────────────────────

func (c *Client) AdminListHypervisorGroups(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisor/groups", nil)
}

func (c *Client) AdminGetHypervisorGroup(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/group/"+url.PathEscape(id))
}

func (c *Client) AdminGetHypervisorGroupHypervisors(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisor/group/"+url.PathEscape(id)+"/hypervisors", nil)
}

// ── hypervisor storages / backup storages / backup plans ─────────────────────

func (c *Client) AdminListHypervisorStorages(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisor/storages", nil)
}

func (c *Client) AdminGetHypervisorStorage(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/storage/"+url.PathEscape(id))
}

func (c *Client) AdminListBackupStorages(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisor/backup-storages", nil)
}

func (c *Client) AdminGetBackupStorage(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/backup-storage/"+url.PathEscape(id))
}

func (c *Client) AdminListBackupPlans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/hypervisor/backup-plans", nil)
}

func (c *Client) AdminGetBackupPlan(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/hypervisor/backup-plan/"+url.PathEscape(id))
}

// ── network: subnets, ips, vpcs ──────────────────────────────────────────────

func (c *Client) AdminListSubnets(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/subnets", nil)
}

func (c *Client) AdminGetSubnet(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/subnet/"+url.PathEscape(id))
}

func (c *Client) AdminGetSubnetIPs(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/subnet/"+url.PathEscape(id)+"/ips", nil)
}

func (c *Client) AdminGetSubnetAvailableIPs(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/subnet/"+url.PathEscape(id)+"/available-ips", nil)
}

func (c *Client) AdminGetSubnetStatistics(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/subnet/"+url.PathEscape(id)+"/statistics")
}

func (c *Client) AdminListIPs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/ips", nil)
}

func (c *Client) AdminListVpcs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/vpcs", nil)
}

func (c *Client) AdminGetVpc(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/vpc/"+url.PathEscape(id))
}

// ── catalog / plans ──────────────────────────────────────────────────────────

func (c *Client) AdminListInstancePlans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/instance/plans", nil)
}

func (c *Client) AdminGetInstancePlan(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/instance/plan/"+url.PathEscape(id))
}

func (c *Client) AdminListISOs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/isos", nil)
}

func (c *Client) AdminListImages(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/images", nil)
}

func (c *Client) AdminListLbPlans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/lb/plans", nil)
}

func (c *Client) AdminListDbPlans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/db/plans", nil)
}

func (c *Client) AdminListVolumePlans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/volume/plans", nil)
}

func (c *Client) AdminListS3Plans(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/s3/plans", nil)
}

func (c *Client) AdminListCsLocations(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/cloud-service/locations", nil)
}

func (c *Client) AdminGetCsLocation(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/cloud-service/location/"+url.PathEscape(id))
}

func (c *Client) AdminListCsPlanGroups(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/cloud-service/plan-groups", nil)
}

func (c *Client) AdminListCurrencies(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/cloud-service/currencies", nil)
}

// ── s3 servers ───────────────────────────────────────────────────────────────

func (c *Client) AdminListS3Servers(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/s3/servers", nil)
}

func (c *Client) AdminGetS3Server(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/s3/server/"+url.PathEscape(id))
}

func (c *Client) AdminListS3Buckets(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/s3/buckets", nil)
}

func (c *Client) AdminListS3AccessKeys(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/s3/access-keys", nil)
}

// ── self-provisioning packs ──────────────────────────────────────────────────

func (c *Client) AdminListSpPacks(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/self-provisioning/packs", nil)
}

func (c *Client) AdminGetSpPack(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/self-provisioning/pack/"+url.PathEscape(id))
}

// ── migrations ───────────────────────────────────────────────────────────────

func (c *Client) AdminListMigrations(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/migrations", nil)
}

func (c *Client) AdminGetMigration(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/migration/"+url.PathEscape(id))
}

func (c *Client) AdminGetMigrationLogs(ctx context.Context, id string) (map[string]any, error) {
	return c.adminGet(ctx, "/v1/migration/"+url.PathEscape(id)+"/logs")
}

// ── system logs ──────────────────────────────────────────────────────────────

func (c *Client) AdminGetAdminLogs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/system/admin-logs", nil)
}

func (c *Client) AdminGetEmailLog(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/system/email-log", nil)
}

func (c *Client) AdminGetIPLog(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/system/ip-log", nil)
}

// ── reverse-DNS requests (read + safe approve/reject) ────────────────────────

func (c *Client) AdminListRdnsRequests(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/v1/dns/reverse/requests", nil)
}

// AdminProcessRdnsRequest approves or rejects a pending reverse-DNS request. It
// is a SAFE, reversible workflow mutation: action must be "approve" or "reject"
// (validated server-side), with an optional reason.
func (c *Client) AdminProcessRdnsRequest(ctx context.Context, id, action, reason string, idemKey string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("AdminProcessRdnsRequest: empty id")
	}
	body := map[string]any{"action": action}
	if reason != "" {
		body["reason"] = reason
	}
	return c.doItemWithHeaders(ctx, "POST", "/v1/dns/reverse/request/"+url.PathEscape(id), body, "", idemHeaders(idemKey))
}

// idemHeaders builds the optional Idempotency-Key header map for a mutation. An
// empty key yields nil (no header). The admin API may not honor it, but sending
// it is harmless and keeps mutations idempotent where the server does support it.
func idemHeaders(idemKey string) map[string]string {
	if idemKey == "" {
		return nil
	}
	return map[string]string{"Idempotency-Key": idemKey}
}
