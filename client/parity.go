package client

import (
	"context"
	"net/url"
)

// Parity methods: additive client methods for the MCP server's coverage-parity
// tools (user API endpoints that had no client method yet). Verified against
// Master routes/user_api.php + the UserApi controllers. The OpenTofu provider
// does not call these; they exist so the MCP tool surface reaches provider
// parity (spec 17). Grouped here to keep the addition reviewable in one place.

// idemKeyOrNew returns idemKey, or a fresh UUID when empty, so an idempotent
// write is never sent without a key (mirrors the k8s create/delete pattern).
func idemKeyOrNew(idemKey string) string {
	if idemKey == "" {
		return newUUIDv4()
	}
	return idemKey
}

// namedList fetches an endpoint whose success body is {key: [ ...objects... ]}
// (a bare Eloquent collection under a named key, not a paginator) and returns
// the array. An absent/mistyped key yields an empty slice, not an error.
func (c *Client) namedList(ctx context.Context, method, path, key string) ([]map[string]any, error) {
	env, err := c.doItem(ctx, method, path, nil, "")
	if err != nil {
		return nil, err
	}
	return objSlice(env[key]), nil
}

// namedPaginatorList fetches an endpoint whose success body nests a Laravel
// paginator under a named key ({key: {data: [...], current_page, ...}}) and
// returns the first page's data array.
func (c *Client) namedPaginatorList(ctx context.Context, method, path, key string) ([]map[string]any, error) {
	env, err := c.doItem(ctx, method, path, nil, "")
	if err != nil {
		return nil, err
	}
	pag, _ := env[key].(map[string]any)
	if pag == nil {
		return []map[string]any{}, nil
	}
	return objSlice(pag["data"]), nil
}

// objSlice coerces a decoded JSON array value ([]any of maps) into
// []map[string]any, dropping non-objects.
func objSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func inst(id string) string { return "/instance/" + url.PathEscape(id) }
func k8s(id string) string  { return "/kubernetes/cluster/" + url.PathEscape(id) }
func lb(id string) string   { return "/load-balancer/" + url.PathEscape(id) }
func vol(id string) string  { return "/storage/volume/" + url.PathEscape(id) }
func db(id string) string   { return "/database/" + url.PathEscape(id) }

// ── managed database actions ─────────────────────────────────────────────────

func (c *Client) BackupManagedDatabase(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "POST", db(id)+"/backup", nil, "backup")
}

func (c *Client) PromoteManagedDatabase(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", db(id)+"/promote", nil)
}

func (c *Client) RestoreManagedDatabase(ctx context.Context, id string, body map[string]any) error {
	return c.doVoid(ctx, "POST", db(id)+"/restore", body)
}

func (c *Client) RestoreManagedDatabasePitr(ctx context.Context, id string, body map[string]any) error {
	return c.doVoid(ctx, "POST", db(id)+"/restore-pitr", body)
}

func (c *Client) RetryManagedDatabasePitr(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", db(id)+"/retry-pitr", nil)
}

func (c *Client) ApplyDatabaseParameterGroup(ctx context.Context, id string, body map[string]any) error {
	return c.doVoid(ctx, "PATCH", db(id)+"/parameter-group", body)
}

// ── backup policy actions ────────────────────────────────────────────────────

func (c *Client) ResetInstanceBackupPolicyFailures(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/backup-policy/"+url.PathEscape(id)+"/reset-failures", nil)
}

func (c *Client) ResetDBBackupPolicyFailures(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/networking/db-backup-policy/"+url.PathEscape(id)+"/reset-failures", nil)
}

// TestDBBackupPolicyConnection runs a live S3 connectivity check. Returns the
// bare {success,message} envelope (200 on success, 422 on failure).
func (c *Client) TestDBBackupPolicyConnection(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/networking/db-backup-policies/test-connection", body, "")
}

// ── volume backup / snapshot restore (async: returns a "queue" tracker) ──────

func (c *Client) CreateVolumeBackup(ctx context.Context, volumeID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", vol(volumeID)+"/backup", body, "queue")
}

func (c *Client) DeleteVolumeBackup(ctx context.Context, volumeID, backupID string) (map[string]any, error) {
	return c.doItem(ctx, "DELETE", vol(volumeID)+"/backup/"+url.PathEscape(backupID), nil, "queue")
}

func (c *Client) RestoreVolumeBackup(ctx context.Context, volumeID, backupID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", vol(volumeID)+"/backup/"+url.PathEscape(backupID)+"/restore", body, "queue")
}

func (c *Client) RestoreVolumeSnapshot(ctx context.Context, volumeID, snapshotID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", vol(volumeID)+"/snapshot/"+url.PathEscape(snapshotID)+"/restore", body, "queue")
}

// ── kubernetes worker / cluster actions (idempotency.user) ───────────────────

func (c *Client) idemItem(ctx context.Context, method, path string, body any, key, idemKey string) (map[string]any, error) {
	return c.doItemWithHeaders(ctx, method, path, body, key, map[string]string{"Idempotency-Key": idemKeyOrNew(idemKey)})
}

func (c *Client) AcknowledgeK8sClusterError(ctx context.Context, clusterID, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/acknowledge-error", nil, "", idemKey)
}

func (c *Client) AcknowledgeK8sKubeconfig(ctx context.Context, clusterID, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/kubeconfig/acknowledge", nil, "", idemKey)
}

func (c *Client) CancelK8sPoolPending(ctx context.Context, clusterID, poolID string, body map[string]any, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/pool/"+url.PathEscape(poolID)+"/cancel-pending", body, "vm_ref", idemKey)
}

func (c *Client) ReassignK8sPool(ctx context.Context, clusterID, poolID string, body map[string]any, idemKey string) (map[string]any, error) {
	// Response carries default_pool_id as a scalar, so return the bare envelope.
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/pool/"+url.PathEscape(poolID)+"/reassign", body, "", idemKey)
}

func (c *Client) DeleteK8sWorkerNode(ctx context.Context, clusterID, nodeName, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "DELETE", k8s(clusterID)+"/worker/"+url.PathEscape(nodeName), nil, "", idemKey)
}

func (c *Client) ToggleK8sWorkersAutoscaling(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/workers/autoscaling", body, "cluster", idemKey)
}

func (c *Client) UpdateK8sWorkersLabels(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "PATCH", k8s(clusterID)+"/workers/labels", body, "", idemKey)
}

func (c *Client) ScaleK8sWorkers(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	return c.idemItem(ctx, "POST", k8s(clusterID)+"/workers/scale", body, "", idemKey)
}

// ── load balancer actions ────────────────────────────────────────────────────

func (c *Client) ListLBSecurityGroupRules(ctx context.Context, lbID, sgID string) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", lb(lbID)+"/security-group/"+url.PathEscape(sgID)+"/rules", "rules")
}

func (c *Client) AddLBSecurityGroupRule(ctx context.Context, lbID, sgID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", lb(lbID)+"/security-group/"+url.PathEscape(sgID)+"/rules", body, "rule")
}

func (c *Client) DeleteLBSecurityGroupRule(ctx context.Context, lbID, sgID, ruleID string) error {
	return c.doVoid(ctx, "DELETE", lb(lbID)+"/security-group/"+url.PathEscape(sgID)+"/rule/"+url.PathEscape(ruleID), nil)
}

func (c *Client) SyncLoadBalancer(ctx context.Context, lbID string) error {
	return c.doVoid(ctx, "POST", lb(lbID)+"/sync", nil)
}

func (c *Client) CreateLBLetsEncryptCertificate(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", lb(lbID)+"/le-certificate", body, "certificate")
}

func (c *Client) RetryLBCertificate(ctx context.Context, lbID, certID string) error {
	return c.doVoid(ctx, "POST", lb(lbID)+"/certificate/"+url.PathEscape(certID)+"/retry", nil)
}

// ── catalog reads ────────────────────────────────────────────────────────────

func (c *Client) ListDbPlans(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/db/plans", "plans")
}

func (c *Client) ListLbPlans(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/load-balancer/plans", "plans")
}

func (c *Client) ListLbLocations(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/load-balancer/locations", "locations")
}

func (c *Client) ListVpcLocations(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/vpc/locations", "locations")
}

func (c *Client) ListVpnGwPlans(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/vpn-gateway/plans", "plans")
}

// ListCurrencies returns the CS currencies (a bare Laravel paginator).
func (c *Client) ListCurrencies(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/cloud-service/currencies", nil)
}

// ── vpn gateway list / retry ─────────────────────────────────────────────────

func (c *Client) ListVpnGateways(ctx context.Context) ([]map[string]any, error) {
	return c.namedPaginatorList(ctx, "GET", "/vpn-gateways", "gateways")
}

// RetryVpnGateway force-redeploys a gateway stuck in "error"; returns the fresh
// gateway (in deploying state).
func (c *Client) RetryVpnGateway(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/vpn-gateway/"+url.PathEscape(id)+"/retry", nil, "gateway")
}

// ── tasks ────────────────────────────────────────────────────────────────────

func (c *Client) ListTasks(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/tasks", nil)
}

func (c *Client) DeleteTask(ctx context.Context, id string) error {
	return c.doVoid(ctx, "DELETE", "/task/"+url.PathEscape(id), nil)
}

// ── misc single actions ──────────────────────────────────────────────────────

func (c *Client) AcknowledgeAlertRule(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/alert-rule/"+url.PathEscape(id)+"/acknowledge", nil)
}

func (c *Client) TestNotificationChannel(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/notification-channel/"+url.PathEscape(id)+"/test", nil)
}

// GenerateSSHKey asks the server to generate a keypair. The response carries the
// stored ssh_key AND the private_key exactly once (never persisted).
func (c *Client) GenerateSSHKey(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/ssh-key/generate", body, "")
}

// GetS3BucketObjectPublicUrl returns {success,url}; the tool reads the "url".
func (c *Client) GetS3BucketObjectPublicUrl(ctx context.Context, bucketID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/object-storage/bucket/"+url.PathEscape(bucketID)+"/object/public-url", body, "")
}

func dnsRec(zoneID, rsID, recID string) string {
	return "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) + "/record/" + url.PathEscape(recID)
}

func (c *Client) SetDnsRecordHealthCheck(ctx context.Context, zoneID, rsID, recID string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", dnsRec(zoneID, rsID, recID)+"/health-check", body, "health_check")
}

func (c *Client) DeleteDnsRecordHealthCheck(ctx context.Context, zoneID, rsID, recID string) error {
	return c.doVoid(ctx, "DELETE", dnsRec(zoneID, rsID, recID)+"/health-check", nil)
}

// ToggleDnsRecord flips the record's enabled flag; returns {success,message,enabled}.
func (c *Client) ToggleDnsRecord(ctx context.Context, zoneID, rsID, recID string) (map[string]any, error) {
	return c.doItem(ctx, "POST", dnsRec(zoneID, rsID, recID)+"/toggle", nil, "")
}

// ── docker deployment control ────────────────────────────────────────────────

func (c *Client) RetryDockerDeployment(ctx context.Context, instanceID, deploymentID string) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(instanceID)+"/docker/"+url.PathEscape(deploymentID)+"/retry", nil, "")
}

func (c *Client) CheckDockerDeploymentStatus(ctx context.Context, instanceID, deploymentID string) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(instanceID)+"/docker/"+url.PathEscape(deploymentID)+"/check-status", nil, "")
}

func (c *Client) ControlDockerDeployment(ctx context.Context, instanceID, deploymentID, action string) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(instanceID)+"/docker/"+url.PathEscape(deploymentID)+"/"+url.PathEscape(action), nil, "")
}

// ── instance sub-resources ───────────────────────────────────────────────────

func (c *Client) ListInstanceBackups(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", inst(id)+"/backups", nil)
}

func (c *Client) CreateInstanceBackup(ctx context.Context, id string, body map[string]any) error {
	return c.doVoid(ctx, "POST", inst(id)+"/backups", body)
}

func (c *Client) BulkDestroyInstanceBackups(ctx context.Context, id string, body map[string]any) error {
	return c.doVoid(ctx, "POST", inst(id)+"/backups/destroy", body)
}

func (c *Client) DeleteInstanceBackup(ctx context.Context, id, backupID string) error {
	return c.doVoid(ctx, "DELETE", inst(id)+"/backup/"+url.PathEscape(backupID), nil)
}

func (c *Client) RestoreInstanceBackup(ctx context.Context, id, backupID string, body map[string]any) error {
	return c.doVoid(ctx, "POST", inst(id)+"/backup/"+url.PathEscape(backupID)+"/restore", body)
}

func (c *Client) ListInstanceDisks(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", inst(id)+"/disks", nil)
}

// InstanceDiskAction runs attach|detach|delete on an instance disk. Returns the
// bare envelope (carrying task_id when the instance is running).
func (c *Client) InstanceDiskAction(ctx context.Context, id, storageID, action string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(id)+"/disk/"+url.PathEscape(storageID)+"/"+url.PathEscape(action), body, "")
}

func (c *Client) ListInstanceIPs(ctx context.Context, id string) ([]map[string]any, error) {
	return c.doList(ctx, "GET", inst(id)+"/ips", nil)
}

func (c *Client) SetInstanceIPRdns(ctx context.Context, id, ipID string, body map[string]any) error {
	return c.doVoid(ctx, "POST", inst(id)+"/ip/"+url.PathEscape(ipID)+"/rdns", body)
}

func (c *Client) EnableInstancePublicInterface(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(id)+"/network/public/enable", nil, "interface")
}

// InstanceISOAction inserts or ejects an ISO on the primary/secondary device.
func (c *Client) InstanceISOAction(ctx context.Context, id, device, action string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(id)+"/iso/"+url.PathEscape(device)+"/"+url.PathEscape(action), body, "")
}

func (c *Client) ForgeEnable(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(id)+"/forge/enable", body, "forge")
}

func (c *Client) ForgeCommit(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", inst(id)+"/forge/commit", nil)
}

func (c *Client) ForgeDiscard(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", inst(id)+"/forge/discard", nil)
}

// GetInstanceDeployImages returns the distro-keyed image map the reinstall UI
// uses ({"Ubuntu":[...],"Debian":[...]}). Returned as the bare object.
func (c *Client) GetInstanceDeployImages(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "GET", inst(id)+"/images", nil, "")
}

// SelfDeployInstance rebuilds a self-provisioned instance from its assigned
// image. Returns the bare envelope carrying the top-level task_id.
func (c *Client) SelfDeployInstance(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", inst(id)+"/self-deploy", body, "")
}
