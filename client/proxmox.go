// Package client - Proxmox-specific methods (spec 17 / ts2 task 1), verified
// against the Master's UserApi\InstanceController (routes/user_api.php) and
// Api\ProxmoxNodeIssueController / Api\InstanceController / Admin\Hypervisor-
// BackupJobController via PveBackupJobService (routes/api.php, prefix
// "api/v1"). These are the shared building blocks the MCP server's Proxmox
// tools (ts2 tasks 2-3) call - no Terraform resource wraps them (v1 of this
// provider does not model VM snapshots, node issues, or PVE-native backup
// jobs as HCL resources).
//
// Envelope shapes, sampled from the controllers/services (not guessed):
//   - ListInstanceSnapshots: GET .../snapshots -> {"success":true,
//     "snapshots":[...]}. The array lives under a NAMED key ("snapshots"),
//     not the generic "data" pagination key doList expects, so this method
//     follows the docker.go dockerIndex/ListDockerDeployments idiom: fetch
//     the bare envelope via doItem(key="") then pull the named array out by
//     hand (extractList).
//   - CreateInstanceSnapshot/RollbackInstanceSnapshot/DeleteInstanceSnapshot/
//     SetInstanceTags/ListInstanceBackupFiles: InstanceService's snapshot/
//     tags/backup-file-restore-list actions all `return response()->json(
//     (array) $result, ...)` where $result is the Hypervisor::sendCommand()
//     reply verbatim (Docs example: '{"success":true}' / '{"success":true,
//     "files":[]}') - there is no separate nested object key to unwrap
//     (unlike, say, docker's "deployment" key), so these use doItem(key="")
//     and hand back the bare envelope, mirroring InstallDockerEngine.
//   - GetInstanceGuestIPs: GET .../guest-ips -> {"success":true,
//     "data":[...]} (docblock-verified in UserApi\InstanceController::
//     guestIps). This DOES use the generic "data" key doList already
//     understands, so it is a plain doList call - no manual unwrap needed.
//   - AdminListNodeIssues: GET /v1/proxmox/node-issues -> a raw Laravel
//     paginator ({"data":[...],"current_page":...}) per Api\
//     ProxmoxNodeIssueController::index (Model::paginate()), same shape
//     family as every other admin INDEX in admin.go - doList.
//   - AdminRetryNodeIssue/AdminResolveNodeIssue: POST .../retry|/resolve ->
//     bare {"success":true,"message":"..."} - doItem(key="").
//   - AdminListBackupJobs: GET .../backup-jobs -> HypervisorBackupJob-
//     Controller::index returns response()->json(['jobs' => ...]) - a NAMED
//     key ("jobs") wrapping a bare (non-paginated) Eloquent collection, same
//     "named array key" shape as ListInstanceSnapshots above - doItem(key="")
//   - extractList(top, "jobs").
//   - AdminCreateBackupJob/AdminUpdateBackupJob/AdminDeleteBackupJob:
//     PveBackupJobService::create/update/delete each `return (array) $result`
//     (the Hypervisor::sendCommand() reply verbatim, or a synthesized
//     {"success":false,"message":"No reachable Proxmox node..."} guard) -
//     doItem(key="").
//   - AdminMigratePrecheck/AdminMigrateInstance: InstanceService::
//     proxmoxMigratePrecheck/proxmoxMigrateAction both `return response()->
//     json((array) $result)` - the sendCommand() reply verbatim - doItem(
//     key="").
package client

import (
	"context"
	"fmt"
	"net/url"
)

// extractList pulls a []map[string]any out of top[key], tolerating the key
// being absent, empty, or holding non-object entries (returns an empty,
// non-nil slice rather than erroring). This is the same manual-unwrap idiom
// docker.go's dockerIndex + ListDockerDeployments use for envelopes that
// carry a named array key ("deployments", "snapshots", "jobs", ...) instead
// of the generic "data" pagination key doList already understands.
func extractList(top map[string]any, key string) []map[string]any {
	raw, _ := top[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

// ── user surface: VM snapshots, tags, guest IPs, backup file browsing ───────

// ListInstanceSnapshots lists the PVE VM snapshots for a Proxmox instance.
// Non-Proxmox instances 422 with {"success":false,"message":"VM snapshots
// are a Proxmox-only feature."}, surfaced as an error by decodeItem's
// success:false check.
func (c *Client) ListInstanceSnapshots(ctx context.Context, instanceID string) ([]map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("ListInstanceSnapshots: empty instance id")
	}
	top, err := c.doItem(ctx, "GET", "/instance/"+url.PathEscape(instanceID)+"/snapshots", nil, "")
	if err != nil {
		return nil, err
	}
	return extractList(top, "snapshots"), nil
}

// CreateInstanceSnapshot creates a PVE VM snapshot. The Master reads the
// snapshot name from the request body's "snapname" field (InstanceService::
// createSnapshot), not "name" - the Go parameter is named `name` for
// caller ergonomics but is sent to the API under the "snapname" key.
func (c *Client) CreateInstanceSnapshot(ctx context.Context, instanceID, name string, vmstate bool) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("CreateInstanceSnapshot: empty instance id")
	}
	if name == "" {
		return nil, fmt.Errorf("CreateInstanceSnapshot: empty snapshot name")
	}
	body := map[string]any{"snapname": name, "vmstate": vmstate}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/snapshot", body, "")
}

// RollbackInstanceSnapshot rolls the instance back to a previously created
// snapshot. Body field is "snapname", matching InstanceService::
// rollbackSnapshot's $request->input('snapname').
func (c *Client) RollbackInstanceSnapshot(ctx context.Context, instanceID, name string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("RollbackInstanceSnapshot: empty instance id")
	}
	if name == "" {
		return nil, fmt.Errorf("RollbackInstanceSnapshot: empty snapshot name")
	}
	body := map[string]any{"snapname": name}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/snapshot/rollback", body, "")
}

// DeleteInstanceSnapshot deletes a PVE VM snapshot. DELETE .../snapshot
// carries a body ({"snapname": name}) - InstanceService::deleteSnapshot
// reads the name from the request body, not the URL, so unlike docker's
// DeleteDockerDeployment this is doItem (with a body), not doVoid.
func (c *Client) DeleteInstanceSnapshot(ctx context.Context, instanceID, name string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("DeleteInstanceSnapshot: empty instance id")
	}
	if name == "" {
		return nil, fmt.Errorf("DeleteInstanceSnapshot: empty snapshot name")
	}
	body := map[string]any{"snapname": name}
	return c.doItem(ctx, "DELETE", "/instance/"+url.PathEscape(instanceID)+"/snapshot", body, "")
}

// SetInstanceTags sets the comma-separated VM tags shown in the Proxmox UI.
// tags may be empty (clears all tags) - only instanceID is required.
func (c *Client) SetInstanceTags(ctx context.Context, instanceID, tags string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("SetInstanceTags: empty instance id")
	}
	body := map[string]any{"tags": tags}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/tags", body, "")
}

// GetInstanceGuestIPs returns the live guest IPs discovered via the QEMU
// guest agent (Proxmox only). Unlike ListInstanceSnapshots this envelope
// already uses the generic "data" key ({"success":true,"data":[...]}), so
// doList handles it directly with no manual unwrap.
func (c *Client) GetInstanceGuestIPs(ctx context.Context, instanceID string) ([]map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("GetInstanceGuestIPs: empty instance id")
	}
	return c.doList(ctx, "GET", "/instance/"+url.PathEscape(instanceID)+"/guest-ips", nil)
}

// ListInstanceBackupFiles browses the file/directory entries inside a PBS-
// backed Proxmox backup at the given path. filepath is optional; when empty
// it is omitted from the query and the Master defaults it to "/". The
// envelope ({"success":true,"files":[...]}) is returned bare - callers read
// the "files" key themselves, matching the map[string]any return type.
func (c *Client) ListInstanceBackupFiles(ctx context.Context, instanceID, backupID, filepath string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("ListInstanceBackupFiles: empty instance id")
	}
	if backupID == "" {
		return nil, fmt.Errorf("ListInstanceBackupFiles: empty backup id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/backup/" + url.PathEscape(backupID) + "/files"
	if filepath != "" {
		q := url.Values{}
		q.Set("filepath", filepath)
		path += "?" + q.Encode()
	}
	return c.doItem(ctx, "GET", path, nil, "")
}

// ── admin surface: node issues, PVE-native backup jobs, cluster migration ───

// AdminListNodeIssues lists Proxmox node operational issues (webssh-proxy
// install failures, tap-bandwidth metering failures, ...). status is
// optional ("open"/"resolved"/...); when empty every issue is returned.
func (c *Client) AdminListNodeIssues(ctx context.Context, status string) ([]map[string]any, error) {
	path := "/v1/proxmox/node-issues"
	if status != "" {
		q := url.Values{}
		q.Set("status", status)
		path += "?" + q.Encode()
	}
	return c.doList(ctx, "GET", path, nil)
}

// AdminRetryNodeIssue re-dispatches the fix job for a node issue (webssh-
// proxy install or tap-bandwidth collection, per the issue's type).
func (c *Client) AdminRetryNodeIssue(ctx context.Context, issueID string) (map[string]any, error) {
	if issueID == "" {
		return nil, fmt.Errorf("AdminRetryNodeIssue: empty issue id")
	}
	return c.doItem(ctx, "POST", "/v1/proxmox/node-issues/"+url.PathEscape(issueID)+"/retry", nil, "")
}

// AdminResolveNodeIssue marks a node issue solved without retrying it.
func (c *Client) AdminResolveNodeIssue(ctx context.Context, issueID string) (map[string]any, error) {
	if issueID == "" {
		return nil, fmt.Errorf("AdminResolveNodeIssue: empty issue id")
	}
	return c.doItem(ctx, "POST", "/v1/proxmox/node-issues/"+url.PathEscape(issueID)+"/resolve", nil, "")
}

// AdminListBackupJobs lists the PVE-native scheduled backup jobs mirrored
// locally for a hypervisor group. The index envelope wraps a bare (non-
// paginated) collection under a "jobs" key, so this is a manual-unwrap
// (extractList), not doList.
func (c *Client) AdminListBackupJobs(ctx context.Context, groupID string) ([]map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("AdminListBackupJobs: empty group id")
	}
	top, err := c.doItem(ctx, "GET", "/v1/hypervisor-group/"+url.PathEscape(groupID)+"/backup-jobs", nil, "")
	if err != nil {
		return nil, err
	}
	return extractList(top, "jobs"), nil
}

// AdminCreateBackupJob creates a PVE-native scheduled backup job. data
// carries the PveBackupJobService::createRules() fields (target_type,
// target_value, storage, schedule, mode, compress, enabled, comment, keep.*).
func (c *Client) AdminCreateBackupJob(ctx context.Context, groupID string, data map[string]any) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("AdminCreateBackupJob: empty group id")
	}
	return c.doItem(ctx, "POST", "/v1/hypervisor-group/"+url.PathEscape(groupID)+"/backup-jobs", data, "")
}

// AdminUpdateBackupJob updates an existing PVE-native backup job. data
// carries the PveBackupJobService::updateRules() fields (all optional).
func (c *Client) AdminUpdateBackupJob(ctx context.Context, groupID, jobID string, data map[string]any) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("AdminUpdateBackupJob: empty group id")
	}
	if jobID == "" {
		return nil, fmt.Errorf("AdminUpdateBackupJob: empty job id")
	}
	path := "/v1/hypervisor-group/" + url.PathEscape(groupID) + "/backup-jobs/" + url.PathEscape(jobID)
	return c.doItem(ctx, "PUT", path, data, "")
}

// AdminDeleteBackupJob deletes a PVE-native backup job.
func (c *Client) AdminDeleteBackupJob(ctx context.Context, groupID, jobID string) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("AdminDeleteBackupJob: empty group id")
	}
	if jobID == "" {
		return nil, fmt.Errorf("AdminDeleteBackupJob: empty job id")
	}
	path := "/v1/hypervisor-group/" + url.PathEscape(groupID) + "/backup-jobs/" + url.PathEscape(jobID)
	return c.doItem(ctx, "DELETE", path, nil, "")
}

// AdminMigratePrecheck runs the pre-migration compatibility check for moving
// a VM to another node in the SAME PVE cluster (maintenance evacuation /
// rebalancing - distinct from the separate KVM InstanceMigrateService flow).
// targetNode is optional; when empty it is omitted from the query and the
// Master lets PVE pick/report on all candidate nodes.
func (c *Client) AdminMigratePrecheck(ctx context.Context, instanceID, targetNode string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("AdminMigratePrecheck: empty instance id")
	}
	path := "/v1/instance/" + url.PathEscape(instanceID) + "/proxmox-migrate/precheck"
	if targetNode != "" {
		q := url.Values{}
		q.Set("target_node", targetNode)
		path += "?" + q.Encode()
	}
	return c.doItem(ctx, "GET", path, nil, "")
}

// AdminMigrateInstance migrates a VM to another node in the same PVE
// cluster. opts must include "target_node" (InstanceService::
// proxmoxMigrateAction reads it from the request body) plus any of online,
// with_local_disks, targetstorage, bwlimit, migration_network,
// with_conntrack_state.
func (c *Client) AdminMigrateInstance(ctx context.Context, instanceID string, opts map[string]any) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("AdminMigrateInstance: empty instance id")
	}
	return c.doItem(ctx, "POST", "/v1/instance/"+url.PathEscape(instanceID)+"/proxmox-migrate", opts, "")
}
